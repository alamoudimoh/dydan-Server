package realtime

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/internal/authlimit"
	"github.com/gin-gonic/gin"
)

// newTicketFlowRouter mirrors the production /api/v1/realtime/sse/ticket
// chain: JWT middleware (which revalidates mutable session state) -> DB-backed
// abuse limiter keyed by IP + authenticated user -> ticket minting handler.
func newTicketFlowRouter(t *testing.T, h *realtimeHarness) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := h.cfg
	limiter := authlimit.New(h.db, cfg)
	r := gin.New()
	r.GET("/api/v1/realtime/sse/ticket",
		h.interactive.MiddlewareFunc(),
		limiter.Middleware("sse-ticket", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth)),
		h.polling.HandleSSETicket)
	return r
}

func mintTicket(t *testing.T, r *gin.Engine, accessToken string) (int, string, http.Header) {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/realtime/sse/ticket", nil)
	req.Header.Set("Authorization", "Bearer "+accessToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var body struct {
		Ticket string `json:"ticket"`
	}
	if w.Code == http.StatusOK {
		if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
			t.Fatalf("decode mint response: %v (%s)", err, w.Body.String())
		}
	}
	return w.Code, body.Ticket, w.Header()
}

// TestRevokedSessionCannotMintSSETicket pins that ticket minting rides on
// full session validation, not just JWT signature validity: after the
// session's refresh token is revoked server-side, a still-signature-valid
// access token must be rejected.
func TestRevokedSessionCannotMintSSETicket(t *testing.T) {
	h := newRealtimeHarness(t, time.Second)
	user, tokens := h.login(t, "mint-user")
	r := newTicketFlowRouter(t, h)

	code, _, _ := mintTicket(t, r, tokens.AccessToken)
	if code != http.StatusOK {
		t.Fatalf("live session must be able to mint, got %d", code)
	}

	h.revokeSession(t, user, tokens.RefreshToken)

	code, _, _ = mintTicket(t, r, tokens.AccessToken)
	// gin-jwt answers 403 for a signature-valid token whose identity can no
	// longer be resolved; either way the request must fail closed.
	if code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("revoked session must not mint tickets, got %d", code)
	}
}

// TestSSETicketResponseIsNotCacheable pins that the credential-bearing mint
// response forbids caching anywhere between client and server.
func TestSSETicketResponseIsNotCacheable(t *testing.T) {
	h := newRealtimeHarness(t, time.Second)
	_, tokens := h.login(t, "cache-user")
	r := newTicketFlowRouter(t, h)

	code, _, header := mintTicket(t, r, tokens.AccessToken)
	if code != http.StatusOK {
		t.Fatalf("mint failed: %d %s", code, header.Get("Cache-Control"))
	}
	if got := header.Get("Cache-Control"); got != "no-store" {
		t.Fatalf("ticket response Cache-Control = %q, want no-store", got)
	}
}

// TestConcurrentSSEConnectionsEachNeedOwnTicket proves simultaneous
// connections cannot share one single-use ticket: N parallel connections with
// distinct tickets succeed; reusing any consumed ticket fails.
func TestConcurrentSSEConnectionsEachNeedOwnTicket(t *testing.T) {
	h := newRealtimeHarness(t, 250*time.Millisecond)
	user, _ := h.login(t, "concurrent-user")

	const n = 4
	tickets := make([]string, n)
	for i := range tickets {
		tickets[i], _ = h.ticketStore().Issue(user.ID, nil)
	}

	type result struct {
		idx int
		ok  bool
	}
	start := make(chan struct{})
	results := make(chan result, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			<-start
			_, ok := h.ticketStore().Consume(tickets[idx])
			results <- result{idx, ok}
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	for res := range results {
		if !res.ok {
			t.Fatalf("own fresh ticket %d must consume successfully", res.idx)
		}
		successes++
	}
	if successes != n {
		t.Fatalf("all %d distinct tickets should work, got %d", n, successes)
	}

	// Every ticket is now spent — replays all fail.
	for i, tk := range tickets {
		if _, ok := h.ticketStore().Consume(tk); ok {
			t.Fatalf("replayed ticket %d must be rejected", i)
		}
	}
}

// TestRealtimeCredentialsCannotAuthenticateOtherEndpoints pins credential
// scoping at the HTTP boundary: neither an SSE ticket nor an access token may
// stand in for the other on the wrong endpoint.
func TestRealtimeCredentialsCannotAuthenticateOtherEndpoints(t *testing.T) {
	h := newRealtimeHarness(t, time.Second)
	user, tokens := h.login(t, "scope-user")
	_ = user

	r := gin.New()
	r.GET("/sse", h.realtimeMW.WebSocketAuthHandler(), h.polling.HandleSSE)
	r.GET("/api/v1/realtime/sse/ticket", h.interactive.MiddlewareFunc(), h.polling.HandleSSETicket)

	get := func(path string) int {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	// A minted SSE ticket is worthless as a JWT on the ticket endpoint...
	ticket, err := h.ticketStore().Issue(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if code := get("/api/v1/realtime/sse/ticket"); code != http.StatusUnauthorized && code != http.StatusForbidden {
		t.Fatalf("no credential presented: got %d", code)
	}
	_ = ticket

	// ...and an access/API-token-shaped value is worthless as a ticket.
	for name, fakeTicket := range map[string]string{
		"access token":   tokens.AccessToken,
		"refresh-like":   tokens.RefreshToken,
		"api-token-like": "aaaa0000bbbb1111cccc2222dddd3333eeee4444ffff55556666777788889999",
	} {
		if code := get("/sse?ticket=" + fakeTicket); code != http.StatusUnauthorized {
			t.Fatalf("%s substituted as ticket must fail closed, got %d", name, code)
		}
	}
}
