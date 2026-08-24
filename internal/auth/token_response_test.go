package auth

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func tokenResponseBody(t *testing.T, origin string) map[string]interface{} {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/token", func(c *gin.Context) {
		WriteTokenResponse(c, http.StatusOK, &TokenResponse{
			AccessToken:        "access",
			RefreshToken:       "refresh-secret",
			AccessTokenExpiry:  time.Now().UTC().Add(time.Minute),
			RefreshTokenExpiry: time.Now().UTC().Add(time.Hour),
			TokenType:          "Bearer",
			Token:              "access",
			Expire:             time.Now().UTC().Add(time.Minute),
		})
	})
	req := httptest.NewRequest(http.MethodPost, "/token", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	var body map[string]interface{}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestWriteTokenResponseOmitsBrowserRefreshCredential(t *testing.T) {
	for _, origin := range []string{"https://app.example", "http://localhost:5173"} {
		body := tokenResponseBody(t, origin)
		if _, ok := body["refresh_token"]; ok {
			t.Fatalf("browser origin %q exposed refresh token", origin)
		}
		if _, ok := body["refresh_token_expiry"]; ok {
			t.Fatalf("browser origin %q exposed refresh token expiry", origin)
		}
	}
}

func TestWriteTokenResponsePreservesNativeAndAPIContract(t *testing.T) {
	for _, origin := range []string{"", "capacitor://localhost", "https://localhost"} {
		body := tokenResponseBody(t, origin)
		if body["refresh_token"] != "refresh-secret" {
			t.Fatalf("origin %q lost body refresh credential", origin)
		}
	}
}
