package auth

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTokenServiceHarness(t *testing.T) (*TokenService, *gorm.DB, *uModel.User) {
	t.Helper()
	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// A single pooled connection keeps every goroutine on the same
	// in-memory database.
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatalf("raw db: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := gdb.AutoMigrate(&uModel.User{}, &uModel.UserSession{}, &uModel.UserNotificationTarget{}, &cModel.Circle{}, &cModel.UserCircle{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{}
	cfg.Jwt.SessionTime = time.Hour
	cfg.Jwt.AccessTokenTime = 15 * time.Minute
	cfg.Jwt.MaxRefresh = 24 * time.Hour
	cfg.Jwt.Secret = "test-secret-that-is-long-enough-1234"

	repo := uRepo.NewUserRepository(gdb, cfg)
	jwtAuth, err := NewAuthMiddleware(cfg, repo, nil)
	if err != nil {
		t.Fatalf("jwt middleware: %v", err)
	}
	svc := NewTokenService(repo, jwtAuth, cfg)

	user := &uModel.User{Username: "rotation-user", CircleID: 1}
	if err := gdb.Create(user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	return svc, gdb, user
}

// familyIDOf resolves the rotation family behind a raw refresh token.
func familyIDOf(t *testing.T, svc *TokenService, db *gorm.DB, refreshToken string) string {
	t.Helper()
	var session uModel.UserSession
	if err := db.Where("token_hash = ?", svc.hashToken(refreshToken)).First(&session).Error; err != nil {
		t.Fatalf("lookup session: %v", err)
	}
	return session.FamilyID
}

func liveFamilySessions(t *testing.T, db *gorm.DB, familyID string) int64 {
	t.Helper()
	var count int64
	db.Model(&uModel.UserSession{}).
		Where("family_id = ? AND revoked_at IS NULL AND expires_at > ?", familyID, time.Now().UTC()).
		Count(&count)
	return count
}

func TestRefreshRotationSingleUse(t *testing.T) {
	svc, db, user := newTokenServiceHarness(t)
	ctx := context.Background()

	first, err := svc.GenerateTokens(ctx, &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatalf("generate tokens: %v", err)
	}

	second, err := svc.RefreshTokens(ctx, first.RefreshToken)
	if err != nil {
		t.Fatalf("first refresh: %v", err)
	}
	if second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh token must rotate on every use")
	}

	// Immediate replay of the just-consumed token is a benign concurrent
	// refresh and must NOT revoke the family.
	third, err := svc.RefreshTokens(ctx, first.RefreshToken)
	if err != nil {
		t.Fatalf("concurrent refresh should succeed within grace window: %v", err)
	}
	if third.RefreshToken == second.RefreshToken {
		t.Fatal("concurrent refresh must still mint a distinct refresh token")
	}
	if liveFamilySessions(t, db, familyIDOf(t, svc, db, second.RefreshToken)) == 0 {
		t.Fatal("family should remain alive after benign concurrent refresh")
	}
}

func TestRefreshReplayAfterGraceRevokesFamily(t *testing.T) {
	svc, db, user := newTokenServiceHarness(t)
	ctx := context.Background()

	first, err := svc.GenerateTokens(ctx, &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatalf("generate tokens: %v", err)
	}
	family := familyIDOf(t, svc, db, first.RefreshToken)

	if _, err := svc.RefreshTokens(ctx, first.RefreshToken); err != nil {
		t.Fatalf("first refresh: %v", err)
	}

	// Age every session beyond the grace window so replaying the original
	// token can no longer be confused with an in-flight concurrent refresh.
	if err := db.Model(&uModel.UserSession{}).
		Where("family_id = ?", family).
		Update("created_at", time.Now().UTC().Add(-2*defaultRefreshGraceWindow)).Error; err != nil {
		t.Fatalf("backdate sessions: %v", err)
	}

	if _, err := svc.RefreshTokens(ctx, first.RefreshToken); err == nil {
		t.Fatal("replay after grace window must be rejected")
	}

	if _, err := svc.RefreshTokens(ctx, first.RefreshToken); err == nil {
		t.Fatal("revoked family must reject refresh")
	}
	if got := liveFamilySessions(t, db, family); got != 0 {
		t.Fatalf("expected entire family revoked, got %d live sessions", got)
	}
}

func TestRefreshConcurrentRequestsAllSucceedWithoutFamilyRevocation(t *testing.T) {
	svc, db, user := newTokenServiceHarness(t)
	ctx := context.Background()

	initial, err := svc.GenerateTokens(ctx, &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatalf("generate tokens: %v", err)
	}
	family := familyIDOf(t, svc, db, initial.RefreshToken)

	const tabs = 8
	var wg sync.WaitGroup
	var mu sync.Mutex
	rotated := make(map[string]bool)
	failures := make([]error, 0)

	for i := 0; i < tabs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := svc.RefreshTokens(ctx, initial.RefreshToken)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				failures = append(failures, err)
				return
			}
			rotated[resp.RefreshToken] = true
		}()
	}
	wg.Wait()

	if len(failures) != 0 {
		t.Fatalf("concurrent refreshes must not fail under the grace window: %v", failures)
	}
	if len(rotated) != tabs {
		t.Fatalf("expected %d distinct rotated refresh tokens, got %d", tabs, len(rotated))
	}
	if liveFamilySessions(t, db, family) == 0 {
		t.Fatal("family was revoked by concurrent refreshes")
	}
}

func TestRevokeOtherAndAllSessions(t *testing.T) {
	svc, db, user := newTokenServiceHarness(t)
	ctx := context.Background()

	a, err := svc.GenerateTokens(ctx, &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatalf("tokens A: %v", err)
	}
	b, err := svc.GenerateTokens(ctx, &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatalf("tokens B: %v", err)
	}

	sessionAID := svc.SessionIDForToken(ctx, a.RefreshToken)
	if sessionAID == "" {
		t.Fatal("session id lookup failed for active token")
	}
	if err := svc.RevokeOtherUserSessions(ctx, user.ID, sessionAID); err != nil {
		t.Fatalf("revoke others: %v", err)
	}

	liveByToken := func(tok string) int64 {
		var n int64
		db.Model(&uModel.UserSession{}).
			Where("token_hash = ? AND revoked_at IS NULL", svc.hashToken(tok)).
			Count(&n)
		return n
	}
	if got := liveByToken(b.RefreshToken); got != 0 {
		t.Fatal("other session should have been revoked")
	}
	if got := liveByToken(a.RefreshToken); got != 1 {
		t.Fatal("own session must survive RevokeOtherUserSessions")
	}

	if err := svc.RevokeAllUserTokens(ctx, user.ID); err != nil {
		t.Fatalf("revoke all: %v", err)
	}
	if got := liveByToken(a.RefreshToken); got != 0 {
		t.Fatalf("expected zero live sessions after revoke-all, got %d", got)
	}
}
