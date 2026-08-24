package user

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"

	auth "github.com/alamoudimoh/daydan-server/internal/auth"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type passwordChangeHarness struct {
	handler *Handler
	router  *gin.Engine
	db      *gorm.DB
	user    *uModel.User
}

func newPasswordChangeHarness(t *testing.T) *passwordChangeHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
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
	cfg.Jwt.MaxRefresh = 24 * time.Hour
	cfg.Jwt.Secret = "test-secret-that-is-long-enough-1234"
	repo := uRepo.NewUserRepository(gdb, cfg)
	jwtAuth, err := auth.NewAuthMiddleware(cfg, repo, nil)
	if err != nil {
		t.Fatalf("jwt middleware: %v", err)
	}
	tokenService := auth.NewTokenService(repo, jwtAuth, cfg)

	hashed, err := auth.EncodePassword("old-password-123")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	user := &uModel.User{
		Username: "pw-user",
		Password: hashed,
		CircleID: 1,
	}
	if err := gdb.Create(user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}

	h := &Handler{
		userRepo:     repo,
		tokenService: tokenService,
	}

	r := gin.New()
	// Stand-in for the JWT middleware: authenticates the seeded user.
	r.Use(func(c *gin.Context) {
		details := &uModel.UserDetails{User: *user}
		auth.SetCurrentUser(c, details)
		c.Next()
	})
	r.PUT("/api/v1/users/change_password", h.updateUserPasswordLoggedInOnly)

	return &passwordChangeHarness{handler: h, router: r, db: gdb, user: user}
}

func (th *passwordChangeHarness) put(t *testing.T, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPut, "/api/v1/users/change_password", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	th.router.ServeHTTP(w, req)
	return w
}

func (th *passwordChangeHarness) newSession(t *testing.T) string {
	t.Helper()
	resp, err := th.handler.tokenService.GenerateTokens(context.Background(), &uModel.UserDetails{User: *th.user})
	if err != nil {
		t.Fatalf("generate tokens: %v", err)
	}
	return resp.RefreshToken
}

func TestPasswordChangeRequiresCurrentPassword(t *testing.T) {
	th := newPasswordChangeHarness(t)

	// Missing current_password field must be rejected outright.
	w := th.put(t, map[string]string{"password": "new-password-123"})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("missing current_password must be rejected with 400, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestPasswordChangeRejectsWrongCurrentPassword(t *testing.T) {
	th := newPasswordChangeHarness(t)
	w := th.put(t, map[string]string{
		"password":         "new-password-123",
		"current_password": "wrong-password",
	})
	if w.Code != http.StatusForbidden {
		t.Fatalf("wrong current password must yield 403, got %d (%s)", w.Code, w.Body.String())
	}
	// The old password must still work after the failed attempt.
	stored, err := th.handler.userRepo.GetUserByID(context.Background(), th.user.ID)
	if err != nil {
		t.Fatalf("load user: %v", err)
	}
	if auth.Matches(stored.Password, "wrong-password") == nil {
		t.Fatal("password must be unchanged after failed attempt")
	}
}

func TestPasswordChangeSucceedsWithCorrectCurrentPasswordAndRevokesOtherSessions(t *testing.T) {
	th := newPasswordChangeHarness(t)

	mine := th.newSession(t)  // stays alive
	other := th.newSession(t) // must die
	_ = other

	w := th.put(t, map[string]string{
		"password":         "new-password-123",
		"current_password": "old-password-123",
		"refresh_token":    mine,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("correct current password must succeed, got %d (%s)", w.Code, w.Body.String())
	}

	hashToken := func(tok string) string {
		sum := sha256.Sum256([]byte(tok))
		return hex.EncodeToString(sum[:])
	}
	liveFor := func(tok string) int64 {
		var n int64
		th.db.Raw(
			`SELECT count(*) FROM user_sessions WHERE token_hash = ? AND revoked_at IS NULL`,
			hashToken(tok),
		).Scan(&n)
		return n
	}
	if liveFor(mine) != 1 {
		t.Fatal("caller's own session must survive a password change")
	}
	if liveFor(other) != 0 {
		t.Fatal("other sessions must be revoked by a password change")
	}
	var updated uModel.User
	if err := th.db.First(&updated, th.user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.CredentialVersion != th.user.CredentialVersion+1 {
		t.Fatalf("credential version=%d want=%d", updated.CredentialVersion, th.user.CredentialVersion+1)
	}
}
