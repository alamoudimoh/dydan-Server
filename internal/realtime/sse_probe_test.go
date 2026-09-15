package realtime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// newSSEProbeHarness builds a minimal repository-backed fixture for the
// bounded SSE session-revalidation probes.
func newSSEProbeHarness(t *testing.T) (*gorm.DB, *uRepo.UserRepository) {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(filepath.Join(t.TempDir(), "sse-probe.db")) + "?_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserSession{}, &cModel.Circle{}, &uModel.UserNotificationTarget{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{}
	repo := uRepo.NewUserRepository(db, cfg)
	return db, repo
}

func liveClaims(username string) map[string]interface{} {
	return map[string]interface{}{
		"id":  username,
		"cv":  0,
		"exp": time.Now().UTC().Add(time.Hour).Unix(),
	}
}

func TestSSEProbeAcceptsHealthySession(t *testing.T) {
	_, repo := newSSEProbeHarness(t)
	user := &uModel.User{Username: "probe-live", Email: "probe-live@example.com"}
	if _, err := repo.CreateUser(context.Background(), user); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := validateSSESession(ctx, repo, liveClaims("probe-live")); err != nil {
		t.Fatalf("healthy session must validate, got error: %v", err)
	}
}

func TestSSEProbeRejectsRevokedOrInvalidState(t *testing.T) {
	_, repo := newSSEProbeHarness(t)

	cases := []struct {
		name   string
		claims map[string]interface{}
	}{
		{
			name:   "unknown user",
			claims: liveClaims("no-such-user"),
		},
		{
			name: "expired access claims",
			claims: map[string]interface{}{
				"id":  "anyone",
				"cv":  0,
				"exp": time.Now().UTC().Add(-time.Minute).Unix(),
			},
		},
		{
			name: "missing credential version",
			claims: map[string]interface{}{
				"id":  "anyone",
				"exp": time.Now().UTC().Add(time.Hour).Unix(),
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := validateSSESession(ctx, repo, tc.claims); err == nil {
				t.Fatal("revoked/invalid state must fail closed")
			}
		})
	}
}

func TestSSEProbeFailsClosedOnRepositoryFailure(t *testing.T) {
	db, repo := newSSEProbeHarness(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("raw db: %v", err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	err = validateSSESession(context.Background(), repo, liveClaims("anyone"))
	if err == nil {
		t.Fatal("repository failure must fail closed")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("repository failure must not be reported as a deadline overrun: %v", err)
	}
}

func TestSSEProbeBoundIsEnforcedFailClosed(t *testing.T) {
	_, repo := newSSEProbeHarness(t)

	// An already-expired parent must propagate into the probe immediately:
	// the deadline is real and any overrun fails closed.
	expired, cancel := context.WithDeadline(context.Background(), time.Now().UTC().Add(-time.Second))
	defer cancel()

	start := time.Now()
	err := validateSSESession(expired, repo, liveClaims("anyone"))
	if err == nil {
		t.Fatal("expired probe context must fail closed")
	}
	if elapsed := time.Since(start); elapsed > realtimeAuthProbeTimeout {
		t.Fatalf("probe ignored its parent deadline (took %v)", elapsed)
	}
}

// TestSSEEventLoopTerminatesWhenValidationFails proves the event loop tears
// the connection down on a failed authoritative probe instead of continuing to
// serve events.
func TestSSEEventLoopTerminatesWhenValidationFails(t *testing.T) {
	gin.SetMode(gin.TestMode)
	_, repo := newSSEProbeHarness(t)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/sse", nil)

	conn := NewConnection("sse-test", 1, 42, &uModel.User{ID: 42}, nil, zap.NewNop().Sugar())
	defer conn.Close()

	loopDone := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		cfg := &config.Config{}
		cfg.RealTimeConfig = config.RealTimeConfig{HeartbeatInterval: 25 * time.Millisecond}
		h := &PollingHandler{
			authMiddleware: &AuthMiddleware{userRepo: repo},
			config:         cfg,
			logger:         zap.NewNop().Sugar(),
		}
		// Claims reference no real user, so the first revalidation tick must
		// terminate the loop.
		h.sseEventLoop(ctx, c, conn, 1, liveClaims("ghost-user"))
		close(loopDone)
	}()

	select {
	case <-loopDone:
	case <-time.After(5 * time.Second):
		t.Fatal("SSE event loop did not terminate after failed validation probe")
	}
}
