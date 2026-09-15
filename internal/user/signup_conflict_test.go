package user

import (
	"context"
	"net/http/httptest"
	"strings"

	auth "github.com/alamoudimoh/dydan-server/internal/auth"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/alamoudimoh/dydan-server/config"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/dydan-server/internal/circle/repo"
	stModel "github.com/alamoudimoh/dydan-server/internal/storage/model"
	storageRepo "github.com/alamoudimoh/dydan-server/internal/storage/repo"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newSignupHarness(t *testing.T) (*Handler, *gorm.DB, *uRepo.UserRepository) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.ToSlash(filepath.Join(t.TempDir(), "signup.db"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.APIToken{}, &cModel.Circle{}, &cModel.UserCircle{}, &stModel.StorageUsage{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := uRepo.NewUserRepository(db, &config.Config{})
	cfg := &config.Config{}
	h := &Handler{
		userRepo:    repo,
		circleRepo:  cRepo.NewCircleRepository(db),
		storageRepo: storageRepo.NewStorageRepository(db, cfg),
	}
	return h, db, repo
}

// encodeTestPassword avoids importing the auth package here.
func encodeTestPassword(pw string) (string, error) { return auth.EncodePassword(pw) }

func seedUser(t *testing.T, repo *uRepo.UserRepository, username, email string) {
	t.Helper()
	hashed, err := encodeTestPassword("irrelevant-password-123")
	if err != nil {
		t.Fatalf("hash password: %v", err)
	}
	if _, err := repo.CreateUser(context.Background(), &uModel.User{
		Username: username,
		Email:    email,
		Password: hashed,
	}); err != nil {
		t.Fatalf("seed user %s: %v", username, err)
	}
}

func TestSignUpDuplicateUsernameIsConflict(t *testing.T) {
	h, _, _ := newSignupHarness(t)

	body := `{"username":"dupe-user","password":"strong-pass-123","email":"first@example.com","displayName":"Dupe"}`
	if code := signUpCode(t, h, body); code != http.StatusCreated {
		t.Fatalf("first signup must succeed with 201, got %d", code)
	}

	replay := `{"username":"dupe-user","password":"another-pass-123","email":"other@example.com","displayName":"Other"}`
	if code := signUpCode(t, h, replay); code != http.StatusConflict {
		t.Fatalf("duplicate username must yield 409 Conflict, got %d", code)
	}
}

func TestSignUpDuplicateEmailIsConflict(t *testing.T) {
	h, _, _ := newSignupHarness(t)

	body := `{"username":"email-one","password":"strong-pass-123","email":"shared@example.com"}`
	if code := signUpCode(t, h, body); code != http.StatusCreated {
		t.Fatalf("first signup must succeed with 201, got %d", code)
	}

	replay := `{"username":"email-two","password":"another-pass-123","email":"shared@example.com"}`
	if code := signUpCode(t, h, replay); code != http.StatusConflict {
		t.Fatalf("duplicate email must yield 409 Conflict, got %d", code)
	}
}

// TestSignUpGenuineFailureIsInternalServerError pins the distinction between
// an expected conflict (409) and a real persistence failure (500): a broken
// users table must not be misreported as a client conflict.
func TestSignUpGenuineFailureIsInternalServerError(t *testing.T) {
	h, db, _ := newSignupHarness(t)
	if err := db.Migrator().DropTable("users"); err != nil {
		t.Fatalf("drop users table: %v", err)
	}

	code := signUpCode(t, h, `{"username":"ghost","password":"strong-pass-123","email":"ghost@example.com"}`)
	if code != http.StatusInternalServerError {
		t.Fatalf("genuine persistence failure must stay 500, got %d", code)
	}
}

func TestCreateUserSentinelErrorClassification(t *testing.T) {
	_, _, repo := newSignupHarness(t)
	seedUser(t, repo, "sentinel", "sentinel@example.com")

	_, err := repo.CreateUser(context.Background(), &uModel.User{
		Username: "sentinel",
		Email:    "other@example.com",
	})
	if err == nil || err != uRepo.ErrUserAlreadyExists {
		t.Fatalf("duplicate identity must classify as ErrUserAlreadyExists, got %v", err)
	}
}

func signUpCode(t *testing.T, h *Handler, body string) int {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/user/signup", strings.NewReader(body))
	h.signUp(c)
	return w.Code
}
