package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	jwtstd "github.com/golang-jwt/jwt/v4"
	"gorm.io/gorm"
)

func openSharedAuthDB(t *testing.T, path string) *gorm.DB {
	t.Helper()
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open shared sqlite: %v", err)
	}
	return db
}

func authStatus(middleware interface{ MiddlewareFunc() gin.HandlerFunc }, token string) int {
	r := gin.New()
	r.Use(middleware.MiddlewareFunc())
	r.GET("/", func(c *gin.Context) { c.Status(http.StatusNoContent) })
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Code
}

func TestAccessRevocationIsSharedImmediateAndRestartSafe(t *testing.T) {
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "auth.db")
	dbA := openSharedAuthDB(t, path)
	dbB := openSharedAuthDB(t, path)
	if err := dbA.AutoMigrate(&uModel.User{}, &uModel.UserSession{}, &uModel.UserPasswordReset{}, &uModel.MFASession{}, &uModel.UserNotificationTarget{}, &cModel.Circle{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{}
	cfg.Jwt.Secret = "shared-revocation-test-secret-123456"
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
	user := &uModel.User{Username: "cross-instance", Email: "cross-instance@example.com", CircleID: 1}
	if err := dbA.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	serviceA := NewTokenService(repoA, middlewareA, cfg)
	tokens, err := serviceA.GenerateTokens(context.Background(), &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatal(err)
	}
	if got := authStatus(middlewareB, tokens.AccessToken); got != http.StatusNoContent {
		t.Fatalf("instance B rejected live token: %d", got)
	}
	otherTokens, err := serviceA.GenerateTokens(context.Background(), &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatal(err)
	}
	_, otherFamily := serviceA.SessionRefForToken(context.Background(), otherTokens.RefreshToken)
	if err := repoA.RevokeOtherUserSessionFamilies(context.Background(), user.ID, otherFamily); err != nil {
		t.Fatalf("revoke other families through A: %v", err)
	}
	if got := authStatus(middlewareB, otherTokens.AccessToken); got != http.StatusNoContent {
		t.Fatalf("preserved family rejected through B: %d", got)
	}
	if got := authStatus(middlewareB, tokens.AccessToken); got == http.StatusNoContent {
		t.Fatalf("revoked unrelated family accepted through B: %d", got)
	}
	if err := repoA.ChangePasswordAndRevokeOtherFamilies(context.Background(), user.ID, "new-password-hash", otherFamily); err != nil {
		t.Fatalf("change password through A: %v", err)
	}
	if got := authStatus(middlewareB, otherTokens.AccessToken); got == http.StatusNoContent {
		t.Fatalf("pre-password-change access token accepted through B: %d", got)
	}
	tokens, err = serviceA.RefreshTokens(context.Background(), otherTokens.RefreshToken)
	if err != nil {
		t.Fatalf("preserved family did not refresh after password change: %v", err)
	}
	if got := authStatus(middlewareB, tokens.AccessToken); got != http.StatusNoContent {
		t.Fatalf("post-password-change access token rejected through B: %d", got)
	}

	revokedFamilyTokens, err := serviceA.GenerateTokens(context.Background(), &uModel.UserDetails{User: uModel.User{
		ID: user.ID, Username: user.Username, Email: user.Email, CircleID: user.CircleID, CredentialVersion: 1,
	}})
	if err != nil {
		t.Fatal(err)
	}
	_, family := serviceA.SessionRefForToken(context.Background(), revokedFamilyTokens.RefreshToken)
	if found, err := repoA.RevokeUserSessionFamily(context.Background(), user.ID, family); err != nil || !found {
		t.Fatalf("revoke through A: found=%v err=%v", found, err)
	}
	if got := authStatus(middlewareB, revokedFamilyTokens.AccessToken); got == http.StatusNoContent {
		t.Fatalf("instance B accepted token after revocation commit: %d", got)
	}
	if got := authStatus(middlewareB, tokens.AccessToken); got != http.StatusNoContent {
		t.Fatalf("unrelated live family rejected through B: %d", got)
	}

	if err := repoA.SetPasswordResetToken(context.Background(), user.Email, "reset-secret"); err != nil {
		t.Fatalf("set password reset: %v", err)
	}
	if err := repoA.UpdatePasswordByToken(context.Background(), user.Email, "reset-secret", "reset-password-hash"); err != nil {
		t.Fatalf("reset password through A: %v", err)
	}
	if got := authStatus(middlewareB, tokens.AccessToken); got == http.StatusNoContent {
		t.Fatalf("pre-reset access token accepted through B: %d", got)
	}

	dbC := openSharedAuthDB(t, path)
	repoC := uRepo.NewUserRepository(dbC, cfg)
	middlewareC, err := NewAuthMiddleware(cfg, repoC, nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := authStatus(middlewareC, tokens.AccessToken); got == http.StatusNoContent {
		t.Fatalf("restarted instance accepted revoked token: %d", got)
	}

	var wg sync.WaitGroup
	for i := 0; i < 24; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if got := authStatus(middlewareB, tokens.AccessToken); got == http.StatusNoContent {
				t.Errorf("post-commit concurrent authentication returned %d", got)
			}
		}()
	}
	wg.Wait()
}

func TestAccessClaimsRequireCredentialVersionAndValidateLegacyTokens(t *testing.T) {
	service, db, user := newTokenServiceHarness(t)
	legacy, _, err := service.jwtAuth.TokenGenerator(&uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatal(err)
	}
	if got := authStatus(service.jwtAuth, legacy); got != http.StatusNoContent {
		t.Fatalf("familyless legacy token with cv rejected: %d", got)
	}

	now := time.Now().UTC()
	withoutCV := jwtstd.NewWithClaims(jwtstd.SigningMethodHS256, jwtstd.MapClaims{
		identityKey: user.Username,
		"orig_iat":  now.Unix(),
		"exp":       now.Add(time.Hour).Unix(),
	})
	raw, err := withoutCV.SignedString(service.jwtSecret)
	if err != nil {
		t.Fatal(err)
	}
	if got := authStatus(service.jwtAuth, raw); got == http.StatusNoContent {
		t.Fatalf("token missing cv accepted: %d", got)
	}

	if err := db.Model(&uModel.User{}).Where("id = ?", user.ID).
		Update("credential_version", gorm.Expr("credential_version + 1")).Error; err != nil {
		t.Fatal(err)
	}
	if got := authStatus(service.jwtAuth, legacy); got == http.StatusNoContent {
		t.Fatalf("old legacy token accepted after credential change: %d", got)
	}
}
