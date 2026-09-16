package user

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestUpdateUserPasswordBlockedOnHostedDydanService(t *testing.T) {
	gin.SetMode(gin.TestMode)
	handler := &Handler{isDydanCloud: true}

	router := gin.New()
	router.PUT("/change_password", handler.updateUserPasswordLoggedInOnly)

	req := httptest.NewRequest(http.MethodPut, "/change_password", strings.NewReader(`{"password":"newpassword1"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusForbidden {
		t.Fatalf("expected 403, got %d (body=%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "hosted Dydan service") {
		t.Fatalf("expected hosted Dydan service message, got %s", w.Body.String())
	}
}
