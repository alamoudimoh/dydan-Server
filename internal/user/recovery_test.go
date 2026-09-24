package user

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type recoverySender struct {
	err  error
	sent chan string
}

func (s *recoverySender) SendResetPasswordEmail(_ context.Context, to, _ string) error {
	if s.sent != nil {
		s.sent <- to
	}
	return s.err
}

func (*recoverySender) SendVerificationEmail(string, string) error { return nil }

func (*recoverySender) SendTestEmail(context.Context, string) error { return nil }

func recoveryHarness(t *testing.T, sender *recoverySender) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&cModel.Circle{}, &uModel.User{}, &uModel.UserPasswordReset{}); err != nil {
		t.Fatal(err)
	}
	h := &Handler{userRepo: uRepo.NewUserRepository(db, &config.Config{}), email: sender}
	router := gin.New()
	router.POST("/reset", h.resetPassword)
	return router, db
}

func requestRecovery(t *testing.T, router http.Handler, email string) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]string{"email": email})
	req := httptest.NewRequest(http.MethodPost, "/reset", strings.NewReader(string(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestRecoveryResponseDoesNotRevealAccountOrProvider(t *testing.T) {
	sender := &recoverySender{sent: make(chan string, 1)}
	router, db := recoveryHarness(t, sender)
	users := []uModel.User{
		{Username: "password-user", Email: "password@example.com", Provider: uModel.AuthProviderDydan},
		{Username: "oidc-user", Email: "oidc@example.com", Provider: uModel.AuthProviderOAuth2},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}

	var expected string
	for _, address := range []string{"missing@example.com", "oidc@example.com", "PASSWORD@example.com"} {
		w := requestRecovery(t, router, address)
		if w.Code != http.StatusAccepted {
			t.Fatalf("%s: status = %d", address, w.Code)
		}
		if expected == "" {
			expected = w.Body.String()
		} else if w.Body.String() != expected {
			t.Fatalf("%s received distinguishable response %q", address, w.Body.String())
		}
	}
	select {
	case address := <-sender.sent:
		if address != "password@example.com" {
			t.Fatalf("delivery address = %q", address)
		}
	case <-time.After(time.Second):
		t.Fatal("password recovery was not dispatched")
	}
}

func TestRecoveryDeliveryFailureInvalidatesCredential(t *testing.T) {
	router, db := recoveryHarness(t, &recoverySender{err: errors.New("delivery unavailable")})
	if err := db.Create(&uModel.User{Username: "alice", Email: "alice@example.com"}).Error; err != nil {
		t.Fatal(err)
	}
	if w := requestRecovery(t, router, "alice@example.com"); w.Code != http.StatusAccepted {
		t.Fatalf("status = %d", w.Code)
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		var count int64
		db.Model(&uModel.UserPasswordReset{}).Count(&count)
		if count == 0 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("undelivered reset credential remained valid")
}
