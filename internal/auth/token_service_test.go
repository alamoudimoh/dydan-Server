package auth

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
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

	// Immediate reuse must not mint an attacker-controlled descendant.
	if _, err := svc.RefreshTokens(ctx, first.RefreshToken); !errors.Is(err, ErrRefreshRetry) {
		t.Fatalf("concurrent refresh should request a retry, got: %v", err)
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

	// Age the original use beyond the grace window so replay can no longer be
	// confused with an in-flight concurrent refresh.
	if err := db.Model(&uModel.UserSession{}).
		Where("token_hash = ?", svc.hashToken(first.RefreshToken)).
		Update("used_at", time.Now().UTC().Add(-2*defaultRefreshGraceWindow)).Error; err != nil {
		t.Fatalf("backdate token use: %v", err)
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

func TestRefreshConcurrentRequestsMintOnlyOneSuccessor(t *testing.T) {
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
	retries := 0
	failures := make([]error, 0)

	for i := 0; i < tabs; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			resp, err := svc.RefreshTokens(ctx, initial.RefreshToken)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if errors.Is(err, ErrRefreshRetry) {
					retries++
					return
				}
				failures = append(failures, err)
				return
			}
			rotated[resp.RefreshToken] = true
		}()
	}
	wg.Wait()

	if len(failures) != 0 {
		t.Fatalf("unexpected concurrent refresh failures: %v", failures)
	}
	if len(rotated) != 1 || retries != tabs-1 {
		t.Fatalf("expected one successor and %d retries, got %d successors and %d retries", tabs-1, len(rotated), retries)
	}
	if liveFamilySessions(t, db, family) == 0 {
		t.Fatal("family was revoked by concurrent refreshes")
	}
}

func TestRefreshConcurrentRequestsAcrossSQLiteConnections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "refresh-rotation.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(1)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserSession{}, &uModel.UserNotificationTarget{}, &cModel.Circle{}, &cModel.UserCircle{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{}
	cfg.Jwt.SessionTime = time.Hour
	cfg.Jwt.AccessTokenTime = 15 * time.Minute
	cfg.Jwt.MaxRefresh = 24 * time.Hour
	cfg.Jwt.Secret = "test-secret-that-is-long-enough-1234"
	seedRepo := uRepo.NewUserRepository(db, cfg)
	seedService := NewTokenService(seedRepo, nil, cfg)
	user := &uModel.User{Username: "multi-connection-rotation-user", CircleID: 1}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	initial, err := seedService.GenerateTokens(context.Background(), &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatalf("generate tokens: %v", err)
	}
	family := familyIDOf(t, seedService, db, initial.RefreshToken)

	const requests = 16
	services := make([]*TokenService, requests)
	for i := range services {
		connection, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		if err != nil {
			t.Fatalf("open connection %d: %v", i, err)
		}
		sqlDB, err := connection.DB()
		if err != nil {
			t.Fatalf("get connection %d: %v", i, err)
		}
		sqlDB.SetMaxOpenConns(1)
		t.Cleanup(func() { _ = sqlDB.Close() })
		services[i] = NewTokenService(uRepo.NewUserRepository(connection, cfg), nil, cfg)
	}

	start := make(chan struct{})
	results := make(chan error, requests)
	var wg sync.WaitGroup
	for _, service := range services {
		wg.Add(1)
		go func(service *TokenService) {
			defer wg.Done()
			<-start
			_, err := service.RefreshTokens(context.Background(), initial.RefreshToken)
			results <- err
		}(service)
	}
	close(start)
	wg.Wait()
	close(results)

	successes := 0
	retries := 0
	for err := range results {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrRefreshRetry):
			retries++
		default:
			t.Fatalf("unexpected concurrent refresh error: %v", err)
		}
	}
	if successes != 1 || retries != requests-1 {
		t.Fatalf("expected 1 success and %d retries, got %d successes and %d retries", requests-1, successes, retries)
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

func TestRefreshRejectsDisabledUserAndRevokesFamily(t *testing.T) {
	svc, db, user := newTokenServiceHarness(t)
	ctx := context.Background()
	tokens, err := svc.GenerateTokens(ctx, &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatal(err)
	}
	family := familyIDOf(t, svc, db, tokens.RefreshToken)
	if err := db.Model(user).Update("disabled", true).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := svc.RefreshTokens(ctx, tokens.RefreshToken); err == nil {
		t.Fatal("disabled user refresh must fail")
	}
	if got := liveFamilySessions(t, db, family); got != 0 {
		t.Fatalf("disabled user family remains live: %d", got)
	}
}

func TestRefreshPreservesAbsoluteFamilyExpiry(t *testing.T) {
	svc, db, user := newTokenServiceHarness(t)
	ctx := context.Background()
	first, err := svc.GenerateTokens(ctx, &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatal(err)
	}
	var original uModel.UserSession
	if err := db.Where("token_hash = ?", svc.hashToken(first.RefreshToken)).First(&original).Error; err != nil {
		t.Fatal(err)
	}
	second, err := svc.RefreshTokens(ctx, first.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	var successor uModel.UserSession
	if err := db.Where("token_hash = ?", svc.hashToken(second.RefreshToken)).First(&successor).Error; err != nil {
		t.Fatal(err)
	}
	if !successor.ExpiresAt.Equal(original.ExpiresAt) {
		t.Fatalf("family expiry slid from %v to %v", original.ExpiresAt, successor.ExpiresAt)
	}
}
