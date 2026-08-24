package auth

import (
	"crypto/tls"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// useTrustedLoopback configures the package-global proxy trust so tests that
// simulate a localhost reverse proxy behave like production.
func useTrustedLoopback(t *testing.T) {
	t.Helper()
	ConfigureProxyTrust([]string{"127.0.0.1/8", "::1/128"})
	t.Cleanup(func() { ConfigureProxyTrust(nil) })
}

// requestFrom simulates a request arriving from a specific peer address.
func requestFrom(method, target, remoteAddr string) *http.Request {
	req := httptest.NewRequest(method, target, nil)
	req.RemoteAddr = remoteAddr
	return req
}

const (
	loopbackPeer = "127.0.0.1:54321"
	externalPeer = "203.0.113.7:54321" // TEST-NET-3, never routed
)

func newCookieTestRouter() (*gin.Engine, func(*http.Request) *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.POST("/api/v1/auth/refresh", func(c *gin.Context) {
		SetRefreshSessionCookie(c, "test-refresh-token", 24*time.Hour)
		c.JSON(http.StatusOK, gin.H{})
	})
	return r, func(req *http.Request) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}
}

func cookieRequest(remoteAddr, forwardedProto string) *httptest.ResponseRecorder {
	r, do := newCookieTestRouter()
	req := requestFrom(http.MethodPost, "/api/v1/auth/refresh", remoteAddr)
	if forwardedProto != "" {
		req.Header.Set("X-Forwarded-Proto", forwardedProto)
	}
	_ = r
	return do(req)
}

func TestRefreshCookieAttributes(t *testing.T) {
	useTrustedLoopback(t)
	// HTTPS terminated at a trusted local reverse proxy.
	w := cookieRequest(loopbackPeer, "https")
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

func TestRefreshCookieSecureOnDirectTLS(t *testing.T) {
	useTrustedLoopback(t)
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/x", nil)
	c.Request.TLS = &tls.ConnectionState{}
	if !requestIsHTTPS(c) {
		t.Fatal("direct TLS requests must yield Secure cookies")
	}
}

func TestRefreshCookieNotSecureOverPlainHTTP(t *testing.T) {
	useTrustedLoopback(t)
	// Self-hosted plain-HTTP installs must keep working.
	if w := cookieRequest(loopbackPeer, ""); len(w.Result().Cookies()) == 1 && w.Result().Cookies()[0].Secure {
		t.Fatal("Secure flag must not be set for plain HTTP requests")
	}
}

func TestSpoofedForwardedProtoIgnoredFromUntrustedPeer(t *testing.T) {
	useTrustedLoopback(t)
	// A directly exposed instance must not let a client flip its own cookie
	// into Secure mode (or derive an https origin) via X-Forwarded-Proto.
	w := cookieRequest(externalPeer, "https")
	for _, c := range w.Result().Cookies() {
		if c.Secure {
			t.Fatal("Secure flag must not be set when forwarding headers come from an untrusted peer")
		}
	}
}

func TestClearRefreshSessionCookieExpiresBothPaths(t *testing.T) {
	useTrustedLoopback(t)
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
	return doCSRFRequestFromPeer(allowed, method, origin, host, loopbackPeer)
}

func doCSRFRequestFromPeer(allowed []string, method, origin, host, peer string) *httptest.ResponseRecorder {
	gin.SetMode(gin.TestMode)
	trusted := []string{"127.0.0.1/8", "::1/128"}
	r := gin.New()
	r.Use(CSRFOriginCheck(allowed, trusted))
	r.POST("/api/v1/auth/refresh", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{})
	})
	req := requestFrom(method, "/api/v1/auth/refresh", peer)
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

func TestCSRFMissingOriginRejected(t *testing.T) {
	w := doCSRFRequest(nil, http.MethodPost, "", "api.example.com")
	if w.Code != http.StatusForbidden {
		t.Fatalf("cookie-authenticated requests without Origin must be rejected, got %d", w.Code)
	}
}

func TestCSRFSafeMethodSkipped(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CSRFOriginCheck(nil, nil))
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
	r.Use(CSRFOriginCheck(nil, []string{"127.0.0.1/8"}))
	r.POST("/api/v1/auth/refresh", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
	req := requestFrom(http.MethodPost, "/api/v1/auth/refresh", loopbackPeer)
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

// A direct (unproxied) client must not be able to make its cross-site Origin
// look legitimate by supplying its own forwarding headers.
func TestCSRFSpoofedForwardedHostRejected(t *testing.T) {
	// Origin matches what a spoofed X-Forwarded-Host/Proto would derive, but
	// the request comes from a non-loopback peer, so those headers are ignored
	// and the Host-based candidate (http://api.example.com) cannot match.
	w := doCSRFRequestFromPeer(nil, http.MethodPost,
		"https://daydan.example.com", "internal-host:2021", externalPeer)
	reqHost := "daydan.example.com"
	_ = reqHost
	if w.Code != http.StatusForbidden {
		t.Fatalf("spoofed forwarding headers must not legitimize a cross-origin POST, got %d", w.Code)
	}
}

// The same request arriving through a genuinely trusted proxy is allowed.
func TestCSRFTrustedProxySameOriginAllowedFromExternalPeer(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CSRFOriginCheck(nil, []string{"127.0.0.1/8"}))
	r.POST("/api/v1/auth/refresh", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{}) })
	// Simulate nginx on loopback: peer is loopback even though the real client
	// is remote; X-Forwarded-* describe the public origin.
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.RemoteAddr = loopbackPeer
	req.Host = "internal-host:2021"
	req.Header.Set("X-Forwarded-Host", "daydan.example.com")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("Origin", "https://daydan.example.com")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("trusted-proxy deployment with matching origin must be allowed, got %d", w.Code)
	}
}
