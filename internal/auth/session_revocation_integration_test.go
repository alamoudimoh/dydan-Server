package auth

import (
	"context"
	"net/http"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func ginSetMode() {
	gin.SetMode(gin.TestMode)
}

// liveSessionRows reports whether the given session row is still live.
func liveSessionRows(t *testing.T, db *gorm.DB, sessionID string) (*uModel.UserSession, string, bool) {
	t.Helper()
	var row uModel.UserSession
	err := db.Where("id = ? AND revoked_at IS NULL AND expires_at > ?", sessionID, time.Now().UTC()).First(&row).Error
	if err != nil {
		return nil, "", false
	}
	return &row, row.FamilyID, true
}

func TestIndividualSessionRevocationIsImmediateAndIsolated(t *testing.T) {
	ginSetMode()
	path := filepath.Join(t.TempDir(), "session-revoke.db")
	dbA := openSharedAuthDB(t, path)
	dbB := openSharedAuthDB(t, path)
	if err := dbA.AutoMigrate(&uModel.User{}, &uModel.UserSession{}, &uModel.UserPasswordReset{}, &uModel.MFASession{}, &uModel.UserNotificationTarget{}, &cModel.Circle{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{}
	cfg.Jwt.Secret = "session-revocation-test-secret-123456"
	cfg.Jwt.SessionTime = time.Hour
	cfg.Jwt.AccessTokenTime = 15 * time.Minute
	cfg.Jwt.MaxRefresh = 24 * time.Hour
	repoA := uRepo.NewUserRepository(dbA, cfg)
	repoB := uRepo.NewUserRepository(dbB, cfg)
	middlewareA, err := NewAuthMiddleware(cfg, repoA, nil)
	if err != nil {
		t.Fatal(err)
	}
	middlewareB, err := NewAuthMiddleware(cfg, repoB, nil)
	if err != nil {
		t.Fatal(err)
	}
	user := &uModel.User{Username: "per-session", Email: "per-session@example.com", CircleID: 1}
	if err := dbA.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	serviceA := NewTokenService(repoA, middlewareA, cfg)

	// 1+2. Two independent logins: session A and session B.
	sessionA, err := serviceA.GenerateTokens(context.Background(), &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatal(err)
	}
	sessionB, err := serviceA.GenerateTokens(context.Background(), &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatal(err)
	}
	sidA, famA := serviceA.SessionRefForToken(context.Background(), sessionA.RefreshToken)
	sidB, famB := serviceA.SessionRefForToken(context.Background(), sessionB.RefreshToken)
	if sidA == "" || sidB == "" || famA == famB {
		t.Fatalf("expected two distinct login sessions, got A=%s/%s B=%s/%s", sidA, famA, sidB, famB)
	}

	// 3. Both access JWTs are accepted through the other replica.
	if got := authStatus(middlewareB, sessionA.AccessToken); got != http.StatusNoContent {
		t.Fatalf("JWT A rejected before revocation: %d", got)
	}
	if got := authStatus(middlewareB, sessionB.AccessToken); got != http.StatusNoContent {
		t.Fatalf("JWT B rejected before revocation: %d", got)
	}

	// 4. Revoke ONLY session A.
	found, err := serviceA.RevokeUserSessionByID(context.Background(), user.ID, sidA)
	if err != nil || !found {
		t.Fatalf("revoke session A: found=%v err=%v", found, err)
	}

	// 5. JWT A is dead immediately after commit — even cross-replica.
	if got := authStatus(middlewareB, sessionA.AccessToken); got == http.StatusNoContent {
		t.Fatal("JWT A accepted through second replica after individual-session revocation")
	}
	if got := authStatus(middlewareA, sessionA.AccessToken); got == http.StatusNoContent {
		t.Fatal("JWT A accepted through issuing replica after individual-session revocation")
	}

	// 6. JWT B still works.
	if got := authStatus(middlewareB, sessionB.AccessToken); got != http.StatusNoContent {
		t.Fatalf("JWT B wrongly rejected after sibling revocation: %d", got)
	}

	// Session A's refresh credential must be dead too.
	if _, _, ok := liveSessionRows(t, dbA, sidA); ok {
		t.Fatal("session A row still live after revocation")
	}
	if _, _, ok := liveSessionRows(t, dbA, sidB); !ok {
		t.Fatal("session B row wrongly revoked")
	}

	// Ownership: another user cannot revoke a session they do not own.
	other := &uModel.User{Username: "other-user", Email: "other@example.com", CircleID: 1}
	if err := dbA.Create(other).Error; err != nil {
		t.Fatal(err)
	}
	if found, err := serviceA.RevokeUserSessionByID(context.Background(), other.ID, sidB); err != nil || found {
		t.Fatalf("cross-user session revocation must not succeed: found=%v err=%v", found, err)
	}
	if got := authStatus(middlewareB, sessionB.AccessToken); got != http.StatusNoContent {
		t.Fatalf("JWT B rejected after failed cross-user revocation attempt: %d", got)
	}

	// Family and global revocation continue to work for the surviving family.
	if found, err := repoA.RevokeUserSessionFamily(context.Background(), user.ID, famB); err != nil || !found {
		t.Fatalf("family revoke: found=%v err=%v", found, err)
	}
	if got := authStatus(middlewareB, sessionB.AccessToken); got == http.StatusNoContent {
		t.Fatal("JWT B accepted after family revocation")
	}
}

// TestRotationKeepsUsedSessionsValidButRevocationKillsExactRow pins the
// boundary between rotation (used_at set, row stays authorized) and
// revocation (revoked_at set, that exact row's JWT dies immediately).
func TestRotationKeepsUsedSessionsValidButRevocationKillsExactRow(t *testing.T) {
	ginSetMode()
	path := filepath.Join(t.TempDir(), "rotation-boundary.db")
	db := openSharedAuthDB(t, path)
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserSession{}, &uModel.UserPasswordReset{}, &uModel.MFASession{}, &uModel.UserNotificationTarget{}, &cModel.Circle{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{}
	cfg.Jwt.Secret = "rotation-boundary-test-secret-123456"
	cfg.Jwt.SessionTime = time.Hour
	cfg.Jwt.AccessTokenTime = 15 * time.Minute
	cfg.Jwt.MaxRefresh = 24 * time.Hour
	repo := uRepo.NewUserRepository(db, cfg)
	middleware, err := NewAuthMiddleware(cfg, repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	user := &uModel.User{Username: "rotator", Email: "rotator@example.com", CircleID: 1}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	service := NewTokenService(repo, middleware, cfg)

	first, err := service.GenerateTokens(context.Background(), &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatal(err)
	}

	// Rotate: the first session row becomes used-but-not-revoked; its access
	// JWT remains valid until normal expiry.
	second, err := service.RefreshTokens(context.Background(), first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if got := authStatus(middleware, first.AccessToken); got != http.StatusNoContent {
		t.Fatalf("pre-rotation JWT rejected after rotation: %d", got)
	}
	if got := authStatus(middleware, second.AccessToken); got != http.StatusNoContent {
		t.Fatalf("post-rotation JWT rejected: %d", got)
	}

	// Revoking exactly the current (head) session kills its JWT but leaves
	// the used ancestor's JWT alive, because the ancestor row is still live.
	secondSID, _ := service.SessionRefForToken(context.Background(), second.RefreshToken)
	if found, err := service.RevokeUserSessionByID(context.Background(), user.ID, secondSID); err != nil || !found {
		t.Fatalf("revoke head session: found=%v err=%v", found, err)
	}
	if got := authStatus(middleware, second.AccessToken); got == http.StatusNoContent {
		t.Fatal("head-session JWT survived its own revocation")
	}
	if got := authStatus(middleware, first.AccessToken); got != http.StatusNoContent {
		t.Fatalf("ancestor-session JWT wrongly killed by head revocation: %d", got)
	}

	// Refresh from the revoked head must fail even though the ancestor row is
	// still live (refresh resolves the latest family head).
	if _, err := service.RefreshTokens(context.Background(), second.RefreshToken); err == nil {
		t.Fatal("refresh with revoked session token succeeded")
	}
}

// TestRevokeOthersOnlyTouchesIntendedSessions covers the keep-current
// semantics: every other family dies, the caller's stays fully intact
// including its already-minted access JWTs.
func TestRevokeOthersOnlyTouchesIntendedSessions(t *testing.T) {
	ginSetMode()
	path := filepath.Join(t.TempDir(), "revoke-others.db")
	db := openSharedAuthDB(t, path)
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserSession{}, &uModel.UserPasswordReset{}, &uModel.MFASession{}, &uModel.UserNotificationTarget{}, &cModel.Circle{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{}
	cfg.Jwt.Secret = "revoke-others-test-secret-12345678"
	cfg.Jwt.SessionTime = time.Hour
	cfg.Jwt.AccessTokenTime = 15 * time.Minute
	cfg.Jwt.MaxRefresh = 24 * time.Hour
	repo := uRepo.NewUserRepository(db, cfg)
	middleware, err := NewAuthMiddleware(cfg, repo, nil)
	if err != nil {
		t.Fatal(err)
	}
	user := &uModel.User{Username: "multi-device", Email: "multi@example.com", CircleID: 1}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	service := NewTokenService(repo, middleware, cfg)

	current, err := service.GenerateTokens(context.Background(), &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatal(err)
	}
	_, currentFam := service.SessionRefForToken(context.Background(), current.RefreshToken)

	victims := make([]*TokenResponse, 0, 3)
	for i := 0; i < 3; i++ {
		tok, err := service.GenerateTokens(context.Background(), &uModel.UserDetails{User: *user})
		if err != nil {
			t.Fatal(err)
		}
		victims = append(victims, tok)
	}

	if err := service.RevokeOtherUserFamilies(context.Background(), user.ID, currentFam); err != nil {
		t.Fatal(err)
	}
	for i, victim := range victims {
		if got := authStatus(middleware, victim.AccessToken); got == http.StatusNoContent {
			t.Fatalf("victim %d JWT survived revoke-others", i)
		}
	}
	if got := authStatus(middleware, current.AccessToken); got != http.StatusNoContent {
		t.Fatalf("current-session JWT rejected after revoke-others: %d", got)
	}
}

// TestConcurrentRequestsVsIndividualSessionRevocation hammers validation and
// revocation concurrently: once the revocation commit lands, no later
// validation may accept the token.
func TestConcurrentRequestsVsIndividualSessionRevocation(t *testing.T) {
	ginSetMode()
	path := filepath.Join(t.TempDir(), "concurrent-revoke.db")
	dbA := openSharedAuthDB(t, path)
	dbB := openSharedAuthDB(t, path)
	if err := dbA.AutoMigrate(&uModel.User{}, &uModel.UserSession{}, &uModel.UserPasswordReset{}, &uModel.MFASession{}, &uModel.UserNotificationTarget{}, &cModel.Circle{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{}
	cfg.Jwt.Secret = "concurrent-revoke-test-secret-1234"
	cfg.Jwt.SessionTime = time.Hour
	cfg.Jwt.AccessTokenTime = 15 * time.Minute
	cfg.Jwt.MaxRefresh = 24 * time.Hour
	repoA := uRepo.NewUserRepository(dbA, cfg)
	repoB := uRepo.NewUserRepository(dbB, cfg)
	middlewareA, err := NewAuthMiddleware(cfg, repoA, nil)
	if err != nil {
		t.Fatal(err)
	}
	middlewareB, err := NewAuthMiddleware(cfg, repoB, nil)
	if err != nil {
		t.Fatal(err)
	}
	user := &uModel.User{Username: "racer", Email: "racer@example.com", CircleID: 1}
	if err := dbA.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	service := NewTokenService(repoA, middlewareA, cfg)
	tokens, err := service.GenerateTokens(context.Background(), &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatal(err)
	}
	sid, _ := service.SessionRefForToken(context.Background(), tokens.RefreshToken)

	var wg sync.WaitGroup
	revoked := make(chan struct{})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-revoked:
					if got := authStatus(middlewareB, tokens.AccessToken); got == http.StatusNoContent {
						t.Error("JWT accepted through replica B after its revocation commit")
					}
					return
				default:
				}
				authStatus(middlewareB, tokens.AccessToken)
			}
		}()
	}
	// The revocation commits through replica A while validation goroutines
	// hammer replica B, mirroring independent production replicas.
	if _, err := repoA.RevokeUserSessionByID(context.Background(), user.ID, sid); err != nil {
		t.Fatal(err)
	}
	close(revoked)
	wg.Wait()

	if got := authStatus(middlewareB, tokens.AccessToken); got == http.StatusNoContent {
		t.Fatal("JWT accepted after concurrent revocation settled")
	}
}
