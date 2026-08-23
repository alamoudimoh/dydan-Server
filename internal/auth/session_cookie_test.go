package auth

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func newCookieTestRouter(secureHeader string) (*gin.Engine, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/auth/refresh", func(c *gin.Context) {
		SetRefreshSessionCookie(c, "test-refresh-token", 24*time.Hour)
		c.JSON(http.StatusOK, gin.H{})
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	if secureHeader != "" {
		req.Header.Set("X-Forwarded-Proto", secureHeader)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return r, w
}

func TestRefreshCookieAttributes(t *testing.T) {
	_, w := newCookieTestRouter("https")
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected exactly one cookie, got %d", len(cookies))
	}
	c := cookies[0]
	if c.Name != RefreshTokenCookieName {
		t.Fatalf("unexpected cookie name %q", c.Name)
	}
	if !c.HttpOnly {
		t.Fatal("refresh cookie must be HttpOnly")
	}
	if !c.Secure {
		t.Fatal("refresh cookie must be Secure over HTTPS")
	}
	if c.SameSite != http.SameSiteLaxMode {
		t.Fatalf("refresh cookie must be SameSite=Lax, got %v", c.SameSite)
	}
	if c.Path != "/api/v1" {
		t.Fatalf("refresh cookie path should be restricted to /api/v1, got %q", c.Path)
	}
	if c.MaxAge <= 0 {
		t.Fatalf("refresh cookie should have positive max-age, got %d", c.MaxAge)
	}
}

func TestRefreshCookieNotSecureOverPlainHTTP(t *testing.T) {
	// Self-hosted plain-HTTP installs must keep working.
	_, w := newCookieTestRouter("")
	if cookies := w.Result().Cookies(); len(cookies) == 1 && cookies[0].Secure {
		t.Fatal("Secure flag must not be set for plain HTTP requests")
	}
}

func TestClearRefreshSessionCookieExpiresBothPaths(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/logout", func(c *gin.Context) {
		ClearRefreshSessionCookie(c)
		c.JSON(http.StatusOK, gin.H{})
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/logout", nil))

	paths := map[string]bool{}
	for _, c := range w.Result().Cookies() {
		if c.Name != RefreshTokenCookieName || c.MaxAge >= 0 || c.Value != "" {
			t.Fatalf("expected expired empty refresh cookie, got %+v", c)
		}
		paths[c.Path] = true
	}
	if !paths["/api/v1"] || !paths["/"] {
		t.Fatalf("must clear both current (%s) and legacy (/) cookie paths, got %v", "/api/v1", paths)
	}
}

func doCSRFRequest(allowed []string, method, origin, host string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CSRFOriginCheck(allowed))
	r.POST("/api/v1/auth/refresh", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{})
	})
	req := httptest.NewRequest(method, "/api/v1/auth/refresh", nil)
	if host != "" {
		req.Host = host
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
		if strings.HasPrefix(origin, "https") {
			req.Header.Set("X-Forwarded-Proto", "https")
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestCSRFCrossOriginRejected(t *testing.T) {
	w := doCSRFRequest(nil, http.MethodPost, "https://evil.example.com", "daydan.example.com")
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-site POST must be rejected with 403, got %d", w.Code)
	}
}

func TestCSRFSameOriginAllowed(t *testing.T) {
	w := doCSRFRequest(nil, http.MethodPost, "https://daydan.example.com", "daydan.example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("same-origin POST must be allowed, got %d", w.Code)
	}
}

func TestCSRFConfiguredOriginAllowed(t *testing.T) {
	allowed := []string{"http://localhost:5173"}
	w := doCSRFRequest(allowed, http.MethodPost, "http://localhost:5173", "api.example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("configured CORS origin must be allowed, got %d", w.Code)
	}
}

func TestCSRFMissingOriginAllowed(t *testing.T) {
	// Native apps and API clients send no Origin header.
	w := doCSRFRequest(nil, http.MethodPost, "", "api.example.com")
	if w.Code != http.StatusOK {
		t.Fatalf("requests without Origin must be allowed, got %d", w.Code)
	}
}

func TestCSRFSafeMethodSkipped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CSRFOriginCheck(nil))
	r.GET("/x", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, func() *http.Request {
		req := httptest.NewRequest(http.MethodGet, "/x", nil)
		req.Header.Set("Origin", "https://evil.example.com")
		return req
	}())
	if w.Code != http.StatusOK {
		t.Fatalf("safe methods must skip CSRF check, got %d", w.Code)
	}
}

func TestCSRFProxyAdvertisedOriginAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CSRFOriginCheck(nil))
	r.POST("/api/v1/auth/refresh", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.Host = "internal-host:2021"
	req.Header.Set("X-Forwarded-Host", "daydan.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Origin", "https://daydan.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("origin matching the proxy-advertised public host must be allowed, got %d", w.Code)
	}
}
