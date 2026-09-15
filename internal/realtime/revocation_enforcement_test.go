package realtime

import (
	"bufio"
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	dydanAuth "github.com/alamoudimoh/dydan-server/internal/auth"
	"github.com/alamoudimoh/dydan-server/internal/authlimit"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
	ginJWT "github.com/appleboy/gin-jwt/v2"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	jwtstd "github.com/golang-jwt/jwt/v4"
	"github.com/gorilla/websocket"
	"gorm.io/gorm"
)

type realtimeHarness struct {
	router  *gin.Engine
	db      *gorm.DB
	repo    *uRepo.UserRepository
	service *dydanAuth.TokenService
	cfg     *config.Config
	// mutatingRepo is a second server handle onto the same database. Test-side
	// revocations commit through it, mirroring production where the replica
	// serving connections never shares its gorm handle with the writer.
	mutatingRepo *uRepo.UserRepository
	realtimeMW   *AuthMiddleware
	interactive  *ginJWT.GinJWTMiddleware
	polling      *PollingHandler
	tickets      *TicketStore
}

func (h *realtimeHarness) ticketStore() *TicketStore { return h.tickets }

func newRealtimeHarness(t *testing.T, heartbeat time.Duration) *realtimeHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "realtime.db")) + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserSession{}, &uModel.UserPasswordReset{}, &uModel.MFASession{}, &uModel.UserNotificationTarget{}, &cModel.Circle{}, &authlimit.Bucket{}, &authlimit.CapacityGuard{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	cfg := &config.Config{}
	cfg.Jwt.Secret = "realtime-revocation-test-secret-123456"
	cfg.Jwt.SessionTime = time.Hour
	cfg.Jwt.AccessTokenTime = 15 * time.Minute
	cfg.Jwt.MaxRefresh = 24 * time.Hour
	cfg.RealTimeConfig = config.RealTimeConfig{
		Enabled:               true,
		WebSocketEnabled:      true,
		SSEEnabled:            true,
		HeartbeatInterval:     heartbeat,
		ConnectionTimeout:     time.Minute,
		MinConnectionInterval: 0,
		MaxConnections:        100,
		MaxConnectionsPerUser: 10,
		EventQueueSize:        64,
		CleanupInterval:       30 * time.Minute,
		StaleThreshold:        time.Hour,
	}

	repo := uRepo.NewUserRepository(db, cfg)

	// Second independent handle onto the same database (WAL allows multiple
	// openers), used for test-side writes.
	db2, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open second sqlite handle: %v", err)
	}
	mutatingRepo := uRepo.NewUserRepository(db2, cfg)

	tickets := NewTicketStore(cfg)
	realtimeMW := NewAuthMiddleware(repo, nil, nil, cfg, tickets)
	interactiveMW, err := dydanAuth.NewAuthMiddleware(cfg, repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	tokenService := dydanAuth.NewTokenService(repo, interactiveMW, cfg)

	rts := NewRealTimeService(cfg)
	if err := rts.Start(context.Background()); err != nil {
		t.Fatalf("start realtime service: %v", err)
	}
	t.Cleanup(func() { _ = rts.Stop() })
	polling := NewPollingHandler(rts, realtimeMW, cfg)
	wsHandler := NewWebSocketHandler(rts, realtimeMW, cfg)

	r := gin.New()
	r.GET("/sse", realtimeMW.WebSocketAuthHandler(), polling.HandleSSE)
	r.GET("/ws", realtimeMW.WebSocketAuthHandler(), wsHandler.HandleWebSocket)

	// Give the auth abuse limiter sane bounds for ticket-mint tests.
	cfg.Server.RateLimitAuthPeriod = time.Minute
	cfg.Server.RateLimitAuth = 1000

	return &realtimeHarness{router: r, db: db, repo: repo, service: tokenService, cfg: cfg, mutatingRepo: mutatingRepo, realtimeMW: realtimeMW, interactive: interactiveMW, polling: polling, tickets: tickets}
}

func (h *realtimeHarness) login(t *testing.T, username string) (*uModel.User, *dydanAuth.TokenResponse) {
	t.Helper()
	var user uModel.User
	err := h.db.Where("username = ?", username).First(&user).Error
	if err != nil {
		user = uModel.User{
			Username:   username,
			Email:      username + "@example.com",
			CircleID:   1,
			Expiration: func() *time.Time { t := time.Now().UTC().Add(24 * time.Hour); return &t }(), // plus-member gate
		}
		if err := h.db.Create(&user).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}
	tokens, err := h.service.GenerateTokens(context.Background(), &uModel.UserDetails{User: user})
	if err != nil {
		t.Fatal(err)
	}
	return &user, tokens
}

func (h *realtimeHarness) revokeSession(t *testing.T, user *uModel.User, refreshToken string) {
	t.Helper()
	sid, _ := h.service.SessionRefForToken(context.Background(), refreshToken)
	if sid == "" {
		t.Fatal("session id must resolve")
	}
	found, err := h.mutatingRepo.RevokeUserSessionByID(context.Background(), user.ID, sid)
	if err != nil || !found {
		t.Fatalf("revoke session: found=%v err=%v", found, err)
	}
}

// sseStream is a live SSE response read from a real HTTP server so the test
// never shares mutable state with the handler goroutine.
type sseStream struct {
	events <-chan string
	done   <-chan struct{}
	close  func()
}

func startSSE(t *testing.T, h *realtimeHarness, accessToken string) *sseStream {
	t.Helper()
	server := httptest.NewServer(h.router)
	ctx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		server.URL+"/sse", nil)
	if err != nil {
		server.Close()
		cancel()
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		server.Close()
		cancel()
		t.Fatalf("SSE connect failed: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		server.Close()
		cancel()
		t.Fatalf("SSE connect status %d", resp.StatusCode)
	}

	events := make(chan string, 64)
	done := make(chan struct{})
	go func() {
		defer close(done)
		scanner := bufio.NewScanner(resp.Body)
		for scanner.Scan() {
			line := scanner.Text()
			if strings.HasPrefix(line, "data:") {
				select {
				case events <- line:
				default:
				}
			}
		}
	}()

	return &sseStream{
		events: events,
		done:   done,
		close: func() {
			cancel()
			resp.Body.Close()
			server.Close()
		},
	}
}

func waitForEvent(t *testing.T, s *sseStream, needle string, timeout time.Duration) {
	t.Helper()
	deadline := time.After(timeout)
	for {
		select {
		case ev := <-s.events:
			if strings.Contains(ev, needle) {
				return
			}
		case <-s.done:
			t.Fatalf("stream closed before %q was observed", needle)
		case <-deadline:
			t.Fatalf("timed out waiting for %q", needle)
		}
	}
}

func TestSSERevokePropagationClosesConnection(t *testing.T) {
	h := newRealtimeHarness(t, 25*time.Millisecond)
	user, tokens := h.login(t, "sse-user")

	stream := startSSE(t, h, tokens.AccessToken)
	defer stream.close()

	waitForEvent(t, stream, "connection.established", 2*time.Second)

	h.revokeSession(t, user, tokens.RefreshToken)
	revokedAt := time.Now()

	select {
	case <-stream.done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE handler did not terminate within bound after session revocation")
	}
	if elapsed := time.Since(revokedAt); elapsed > 2*time.Second {
		t.Fatalf("SSE close latency after revoke commit exceeded bound: %v", elapsed)
	}
}

// TestSSEHeartbeatBoundCannotBeDisabled pins the clamping contract: a zero or
// negative configured heartbeat must not disable revocation checks.
func TestSSEHeartbeatBoundCannotBeDisabled(t *testing.T) {
	cases := []struct {
		name     string
		in       time.Duration
		expected time.Duration
	}{
		{"zero falls back to default", 0, defaultHeartbeatInterval},
		{"negative falls back to default", -time.Second, defaultHeartbeatInterval},
		{"huge value clamped to max", 24 * time.Hour, maxHeartbeatInterval},
		{"normal value preserved", 30 * time.Second, 30 * time.Second},
		{"selfhosted value preserved", 60 * time.Second, 60 * time.Second},
	}
	for _, tc := range cases {
		if got := effectiveHeartbeatInterval(tc.in); got != tc.expected {
			t.Errorf("%s: effectiveHeartbeatInterval(%v) = %v, want %v", tc.name, tc.in, got, tc.expected)
		}
	}
}

func TestSSEConnectionSurvivesUnrelatedSessionRevocation(t *testing.T) {
	h := newRealtimeHarness(t, 25*time.Millisecond)
	user, liveTokens := h.login(t, "sse-live")
	_, deadTokens := h.login(t, "sse-live") // same user, different family

	h.revokeSession(t, user, deadTokens.RefreshToken)

	stream := startSSE(t, h, liveTokens.AccessToken)
	defer stream.close()

	waitForEvent(t, stream, "connection.established", 2*time.Second)

	select {
	case <-stream.done:
		t.Fatal("SSE connection closed although its own session was never revoked")
	case <-time.After(300 * time.Millisecond):
		// Survived several heartbeat revalidation ticks — correct.
	}
}

func TestSSEHandshakeRejectsInvalidAndExpiredTokens(t *testing.T) {
	h := newRealtimeHarness(t, 25*time.Millisecond)
	user, _ := h.login(t, "sse-handshake")

	expired := jwtstd.NewWithClaims(jwtstd.SigningMethodHS256, jwtstd.MapClaims{
		"id":       user.Username,
		"cv":       0,
		"orig_iat": time.Now().UTC().Add(-time.Hour).Unix(),
		"exp":      time.Now().UTC().Add(-time.Minute).Unix(),
	})
	expiredRaw, err := expired.SignedString([]byte(h.cfg.Jwt.Secret))
	if err != nil {
		t.Fatal(err)
	}
	wrongSig := jwtstd.NewWithClaims(jwtstd.SigningMethodHS256, jwtstd.MapClaims{
		"id":       user.Username,
		"cv":       0,
		"orig_iat": time.Now().UTC().Unix(),
		"exp":      time.Now().UTC().Add(time.Hour).Unix(),
	})
	wrongSigRaw, err := wrongSig.SignedString([]byte("some-other-secret-1234567890"))
	if err != nil {
		t.Fatal(err)
	}

	for name, token := range map[string]string{
		"garbage":         "not-a-jwt",
		"wrong-signature": wrongSigRaw,
		"expired":         expiredRaw,
	} {
		req := httptest.NewRequest(http.MethodGet, "/sse", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		w := httptest.NewRecorder()
		h.router.ServeHTTP(w, req)
		if w.Code == http.StatusOK {
			t.Fatalf("%s token must not establish SSE connection", name)
		}
	}
}

func TestWebSocketRevocationTerminatesWithinBound(t *testing.T) {
	prevPing := wsPingInterval
	wsPingInterval = 40 * time.Millisecond
	defer func() { wsPingInterval = prevPing }()

	h := newRealtimeHarness(t, time.Second)
	user, tokens := h.login(t, "ws-user")

	server := httptest.NewServer(h.router)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization": []string{"Bearer " + tokens.AccessToken},
	})
	if err != nil {
		status := "<nil>"
		if resp != nil {
			status = resp.Status
		}
		t.Fatalf("websocket dial failed (%v): status %s", err, status)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var established map[string]interface{}
	if err := conn.ReadJSON(&established); err != nil {
		t.Fatalf("read established event: %v", err)
	}

	h.revokeSession(t, user, tokens.RefreshToken)
	revokedAt := time.Now()

	// The write pump must observe the revocation on its next tick and close
	// the socket with a policy-violation close code.
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, _, err = conn.ReadMessage()
	if err == nil {
		t.Fatal("expected connection termination after revocation, got message")
	}
	if elapsed := time.Since(revokedAt); elapsed > 3*time.Second {
		t.Fatalf("websocket termination took too long after revoke commit: %v", elapsed)
	}
	closeErr, ok := err.(*websocket.CloseError)
	if !ok {
		t.Fatalf("expected CloseError, got %T: %v", err, err)
	}
	if closeErr.Code != websocket.ClosePolicyViolation {
		t.Fatalf("expected policy violation close code, got %d (%s)", closeErr.Code, closeErr.Text)
	}
}

func TestWebSocketSurvivesUnrelatedRevocation(t *testing.T) {
	prevPing := wsPingInterval
	wsPingInterval = 30 * time.Millisecond
	defer func() { wsPingInterval = prevPing }()

	h := newRealtimeHarness(t, time.Second)
	user, keepTokens := h.login(t, "ws-keep")
	_, killTokens := h.login(t, "ws-keep")

	server := httptest.NewServer(h.router)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization": []string{"Bearer " + keepTokens.AccessToken},
	})
	if err != nil {
		status := "<nil>"
		if resp != nil {
			status = resp.Status
		}
		t.Fatalf("dial keep connection failed (%v): status %s", err, status)
	}
	defer conn.Close()

	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	var established map[string]interface{}
	if err := conn.ReadJSON(&established); err != nil {
		t.Fatalf("read established event: %v", err)
	}

	h.revokeSession(t, user, killTokens.RefreshToken)

	// Ride out many revalidation ticks: any close frame means the wrong
	// connection was killed. Server pings are consumed transparently by the
	// gorilla read path; short deadlines just yield timeouts, which are fine.
	stillOpen := true
	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		conn.SetReadDeadline(time.Now().Add(25 * time.Millisecond))
		_, _, err := conn.ReadMessage()
		if err != nil && websocket.IsCloseError(err, websocket.ClosePolicyViolation, websocket.CloseNormalClosure, websocket.CloseAbnormalClosure, websocket.CloseGoingAway) {
			stillOpen = false
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !stillOpen {
		t.Fatal("kept WebSocket closed although its own session stayed live")
	}
}

func TestWebSocketHandshakeRejectsInvalidTokens(t *testing.T) {
	h := newRealtimeHarness(t, time.Second)
	server := httptest.NewServer(h.router)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	if _, _, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization": []string{"Bearer garbage-token"},
	}); err == nil {
		t.Fatal("garbage token must be rejected at handshake")
	}
}

// TestWebSocketOutlivesAccessTokenTTLWhileSessionLive pins the revalidation
// contract: ongoing authorization is authoritative DB state, not the
// handshake JWT's expiry, so a live session's connection rides past its
// original token's exp. Revoking the session still kills it immediately.
func TestWebSocketOutlivesAccessTokenTTLWhileSessionLive(t *testing.T) {
	prevPing := wsPingInterval
	wsPingInterval = 30 * time.Millisecond
	defer func() { wsPingInterval = prevPing }()

	h := newRealtimeHarness(t, time.Second)
	user, tokens := h.login(t, "ws-ttl")

	sid, fam := h.service.SessionRefForToken(context.Background(), tokens.RefreshToken)
	if sid == "" {
		t.Fatal("session must resolve")
	}

	// Hand-craft an access JWT for the same session whose exp passes within
	// seconds; the handshake accepts it now but the token ages out during the
	// observation window.
	short := jwtstd.NewWithClaims(jwtstd.SigningMethodHS256, jwtstd.MapClaims{
		"id":       user.Username,
		"cv":       0,
		"orig_iat": time.Now().UTC().Add(-time.Minute).Unix(),
		"exp":      time.Now().UTC().Add(2 * time.Second).Unix(),
		"sid":      sid,
		"fam":      fam,
	})
	shortRaw, err := short.SignedString([]byte(h.cfg.Jwt.Secret))
	if err != nil {
		t.Fatal(err)
	}

	server := httptest.NewServer(h.router)
	defer server.Close()
	wsURL := "ws" + strings.TrimPrefix(server.URL, "http") + "/ws"

	conn, resp, err := websocket.DefaultDialer.Dial(wsURL, http.Header{
		"Authorization": []string{"Bearer " + shortRaw},
	})
	if err != nil {
		status := "<nil>"
		if resp != nil {
			status = resp.Status
		}
		t.Fatalf("dial failed (%v): status %s", err, status)
	}
	defer conn.Close()

	type readResult struct {
		err error
	}
	results := make(chan readResult, 1)
	go func() {
		for {
			conn.SetReadDeadline(time.Now().Add(5 * time.Second))
			if _, _, err := conn.ReadMessage(); err != nil {
				results <- readResult{err}
				return
			}
		}
	}()

	// Established event.
	select {
	case r := <-results:
		t.Fatalf("connection closed before established: %v", r.err)
	case <-time.After(500 * time.Millisecond):
	}

	// Wait well past the 2s token TTL: the connection must survive on
	// authoritative state alone.
	select {
	case r := <-results:
		t.Fatalf("connection closed at %v although its session stayed live: %v", time.Since(startOfObservation()), r.err)
	case <-time.After(3500 * time.Millisecond):
	}

	// Revocation must terminate it within a couple of ping ticks.
	h.revokeSession(t, user, tokens.RefreshToken)
	revokedAt := time.Now()
	select {
	case r := <-results:
		closeErr, ok := r.err.(*websocket.CloseError)
		if !ok || closeErr.Code != websocket.ClosePolicyViolation {
			t.Fatalf("expected policy-violation close after revocation, got %v", r.err)
		}
		if elapsed := time.Since(revokedAt); elapsed > 2*time.Second {
			t.Fatalf("termination took too long after revoke commit: %v", elapsed)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no close after revocation")
	}
}

func startOfObservation() time.Time { return time.Now() }
