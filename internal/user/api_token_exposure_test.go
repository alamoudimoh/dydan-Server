package user

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/alamoudimoh/daydan-server/config"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTokenExposureHarness(t *testing.T) (*Handler, *gorm.DB, *uRepo.UserRepository) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.ToSlash(filepath.Join(t.TempDir(), "api-token.db"))), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.APIToken{}, &cModel.Circle{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	repo := uRepo.NewUserRepository(db, &config.Config{})
	h := &Handler{userRepo: repo}
	return h, db, repo
}

// directCall invokes a handler with a prepared context carrying currentUser.
func directCall(handler gin.HandlerFunc, method string, body []byte, currentUser *uModel.UserDetails) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, "/", bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	if currentUser != nil {
		c.Set("id", currentUser)
	}
	handler(c)
	return w
}

func TestAPITokenSecretDisclosedOnlyAtCreation(t *testing.T) {
	h, _, repo := newTokenExposureHarness(t)

	user := &uModel.User{Username: "token-user", Email: "token-user@example.com"}
	if _, err := repo.CreateUser(context.Background(), user); err != nil {
		t.Fatalf("seed user: %v", err)
	}

	create := directCall(h.CreateLongLivedToken, http.MethodPost, []byte(`{"name":"ci"}`), &uModel.UserDetails{User: *user})
	if create.Code != http.StatusOK {
		t.Fatalf("creation failed: %d %s", create.Code, create.Body.String())
	}

	var created struct {
		Res struct {
			ID        int    `json:"id"`
			Name      string `json:"name"`
			CreatedAt string `json:"createdAt"`
			Token     string `json:"token"`
		} `json:"res"`
	}
	if err := json.Unmarshal(create.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode creation response: %v", err)
	}
	if created.Res.Token == "" || len(created.Res.Token) < 32 {
		t.Fatalf("creation response must disclose the raw credential once: %q", created.Res.Token)
	}
	raw := created.Res.Token

	list := directCall(h.GetAllUserToken, http.MethodGet, nil, &uModel.UserDetails{User: *user})
	if list.Code != http.StatusOK {
		t.Fatalf("listing failed: %d %s", list.Code, list.Body.String())
	}
	if bytes.Contains(list.Body.Bytes(), []byte(raw)) {
		t.Fatal("list endpoint leaked the raw API-token credential")
	}

	var listed struct {
		Res []struct {
			ID        int    `json:"id"`
			Name      string `json:"name"`
			Token     string `json:"token"`
			CreatedAt string `json:"createdAt"`
		} `json:"res"`
	}
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list response: %v", err)
	}
	if len(listed.Res) != 1 {
		t.Fatalf("expected exactly one listed token, got %d", len(listed.Res))
	}
	if listed.Res[0].Token != "" {
		t.Fatalf("listed token entry must not carry secret material, got %q", listed.Res[0].Token)
	}
	if listed.Res[0].Name != "ci" || listed.Res[0].ID == 0 {
		t.Fatalf("list must still expose safe metadata, got %+v", listed.Res[0])
	}

	// The stored credential must still authenticate its owner: the reusable
	// secret remains valid even though it can no longer be read back.
	authed, err := repo.GetUserByToken(context.Background(), raw)
	if err != nil {
		t.Fatalf("API-token authentication broke after exposure fix: %v", err)
	}
	if authed.Username != user.Username {
		t.Fatalf("authenticated wrong user: %+v", authed.Username)
	}
}
