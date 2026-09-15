package auth

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
)

func TestRefreshBodyCredentialOverridesStaleCookie(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service, _, user := newTokenServiceHarness(t)
	tokens, err := service.GenerateTokens(context.Background(), &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatalf("generate tokens: %v", err)
	}
	body, err := json.Marshal(RefreshRequest{RefreshToken: tokens.RefreshToken})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/auth/refresh", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	request.AddCookie(&http.Cookie{Name: RefreshTokenCookieName, Value: "stale"})
	context, _ := gin.CreateTestContext(recorder)
	context.Request = request

	(&AuthHandler{tokenService: service}).RefreshTokenHandler(context)
	if recorder.Code != http.StatusOK {
		t.Fatalf("refresh status = %d, body = %s", recorder.Code, recorder.Body.String())
	}
}
