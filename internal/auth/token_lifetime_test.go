package auth

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
	jwtstd "github.com/golang-jwt/jwt/v4"
)

// TestInteractiveAccessTokensAreShortLived proves that tokens minted by the
// token service (browser PWA + native app paths) carry the configured short
// access-token lifetime instead of the long legacy session time.
func TestInteractiveAccessTokensAreShortLived(t *testing.T) {
	svc, _, user := newTokenServiceHarness(t)
	ctx := context.Background()

	resp, err := svc.GenerateTokens(ctx, &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatalf("generate tokens: %v", err)
	}

	parsed, err := jwtstd.Parse(resp.AccessToken, func(tk *jwtstd.Token) (interface{}, error) {
		return svc.jwtSecret, nil
	})
	if err != nil {
		t.Fatalf("parse access token: %v", err)
	}
	claims := parsed.Claims.(jwtstd.MapClaims)

	iat := int64(claims["orig_iat"].(float64))
	exp := int64(claims["exp"].(float64))
	ttl := time.Duration(exp-iat) * time.Second

	if ttl != svc.accessTokenTime {
		t.Fatalf("interactive access token TTL = %v, want %v", ttl, svc.accessTokenTime)
	}
	if svc.accessTokenTime >= svc.sessionTime {
		t.Fatalf("test setup invalid: interactive lifetime %v must be shorter than legacy %v",
			svc.accessTokenTime, svc.sessionTime)
	}

	// Lineage claims must be present so session management can identify the
	// caller's own session/family.
	sid, fam := SessionRefFromClaims(map[string]interface{}(claims))
	if sid == "" || fam == "" {
		t.Fatalf("access token missing lineage claims, got sid=%q fam=%q", sid, fam)
	}
	if fam != svc.SessionIDFamilyForTest(t, resp.RefreshToken) {
		t.Fatalf("fam claim %q does not match refresh family", fam)
	}
}

// TestLegacyLoginPathKeepsLongLifetime pins the integration-compat contract:
// the raw middleware generator (behind /api/v1/auth/login/legacy) is untouched
// and still issues SessionTime-lived tokens without refresh sessions.
func TestLegacyLoginPathKeepsLongLifetime(t *testing.T) {
	svc, _, user := newTokenServiceHarness(t)

	token, expire, err := svc.jwtAuth.TokenGenerator(&uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatalf("legacy token generation: %v", err)
	}
	ttl := time.Until(expire)
	if ttl > svc.sessionTime || ttl < svc.sessionTime-5*time.Second {
		t.Fatalf("legacy token TTL = %v, want ~%v", ttl, svc.sessionTime)
	}
	if len(token) == 0 {
		t.Fatal("legacy token must not be empty")
	}
}

// TestInteractiveTokenAcceptedByMiddleware proves the shortened JWTs are fully
// compatible with the standard bearer-auth middleware while they are live.
func TestInteractiveTokenAcceptedByMiddleware(t *testing.T) {
	svc, db, user := newTokenServiceHarness(t)
	gin.SetMode(gin.TestMode)

	resp, err := svc.GenerateTokens(context.Background(), &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatalf("generate tokens: %v", err)
	}

	r := gin.New()
	r.Use(svc.jwtAuth.MiddlewareFunc())
	r.GET("/me", func(c *gin.Context) {
		u, ok := CurrentUser(c)
		if !ok || u.Username != user.Username {
			c.JSON(http.StatusForbidden, gin.H{"error": "wrong identity"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"user": u.Username})
	})

	req := httptest.NewRequest(http.MethodGet, "/me", nil)
	req.Header.Set("Authorization", "Bearer "+resp.AccessToken)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("interactive access token must authenticate, got %d: %s", w.Code, w.Body.String())
	}

	_ = db
}

// SessionIDFamilyForTest resolves a refresh token's family ID (test helper).
func (s *TokenService) SessionIDFamilyForTest(t *testing.T, refreshToken string) string {
	t.Helper()
	_, fam := s.SessionRefForToken(context.Background(), refreshToken)
	return fam
}
