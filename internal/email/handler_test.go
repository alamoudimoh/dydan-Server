package email

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alamoudimoh/dydan-server/internal/auth"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/dydan-server/internal/circle/repo"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
)

type testSender struct {
	testRecipient string
	err           error
}

func (s *testSender) SendResetPasswordEmail(context.Context, string, string) error { return nil }
func (s *testSender) SendVerificationEmail(string, string) error                   { return nil }
func (s *testSender) SendTestEmail(_ context.Context, to string) error {
	s.testRecipient = to
	return s.err
}

func emailHandlerForTest(t *testing.T, role cModel.UserRole) (*Handler, *uModel.UserDetails) {
	t.Helper()
	store := newRuntimeStoreForTest(t)
	user := &uModel.UserDetails{User: uModel.User{ID: 7, CircleID: 11, Username: "administrator"}}
	if err := store.db.AutoMigrate(&cModel.UserCircle{}); err != nil {
		t.Fatal(err)
	}
	if err := store.db.Create(&cModel.UserCircle{UserID: user.ID, CircleID: user.CircleID, Role: role, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	return NewHandler(store, &testSender{}, cRepo.NewCircleRepository(store.db)), user
}

func emailHandlerRouter(h *Handler, user *uModel.UserDetails) *gin.Engine {
	r := gin.New()
	r.Use(func(c *gin.Context) {
		if user != nil {
			auth.SetCurrentUser(c, user)
		}
	})
	r.GET("/email-delivery", h.get)
	r.POST("/email-delivery/test", h.test)
	return r
}

func TestEmailDeliveryRequiresAdministrator(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, owner := emailHandlerForTest(t, cModel.UserRoleAdmin)
	for _, tc := range []struct {
		name string
		user *uModel.UserDetails
		want int
	}{
		{name: "anonymous", want: http.StatusUnauthorized},
		{name: "member", user: &uModel.UserDetails{User: uModel.User{ID: owner.ID, CircleID: owner.CircleID}}, want: http.StatusForbidden},
		{name: "administrator", user: owner, want: http.StatusOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.name == "member" {
				// A separate non-admin membership makes the authorization distinction explicit.
				if err := h.store.db.Model(&cModel.UserCircle{}).Where("user_id = ? AND circle_id = ?", owner.ID, owner.CircleID).Update("role", cModel.UserRoleMember).Error; err != nil {
					t.Fatal(err)
				}
			}
			w := httptest.NewRecorder()
			emailHandlerRouter(h, tc.user).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/email-delivery", nil))
			if w.Code != tc.want {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			if tc.name == "member" {
				if err := h.store.db.Model(&cModel.UserCircle{}).Where("user_id = ? AND circle_id = ?", owner.ID, owner.CircleID).Update("role", cModel.UserRoleAdmin).Error; err != nil {
					t.Fatal(err)
				}
			}
		})
	}
}

func TestEmailDeliveryTestEndpointValidatesRecipientAndMasksProviderFailure(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, owner := emailHandlerForTest(t, cModel.UserRoleAdmin)
	sender := h.sender.(*testSender)
	r := emailHandlerRouter(h, owner)

	for _, tc := range []struct {
		body string
		want int
	}{
		{body: `{"to":"not-an-email"}`, want: http.StatusBadRequest},
		{body: `{"to":"admin@example.com"}`, want: http.StatusOK},
	} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/email-delivery/test", strings.NewReader(tc.body)))
		if w.Code != tc.want {
			t.Fatalf("body %s: status = %d, response = %s", tc.body, w.Code, w.Body.String())
		}
	}
	if sender.testRecipient != "admin@example.com" {
		t.Fatalf("test recipient = %q", sender.testRecipient)
	}

	sender.err = context.DeadlineExceeded
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/email-delivery/test", strings.NewReader(`{"to":"admin@example.com"}`)))
	if w.Code != http.StatusBadGateway || strings.Contains(w.Body.String(), "DeadlineExceeded") {
		t.Fatalf("provider failure leaked or had wrong status: %d %s", w.Code, w.Body.String())
	}
}
