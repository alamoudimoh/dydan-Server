package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// TestSessionRestoreBehindHTTPSProxy validates the core requirement: a valid
// session behind HTTPS must NOT become "session expired" simply because TLS
// terminates at a reverse proxy.
//
// Scenario: Browser loads https://example.local, which is proxied through Nginx
// to dydan's internal http://internal:2021. The request path is:
//
//	https://example.local/api/v1/auth/refresh
//	-> [Nginx/TLS termination]
//	-> http://internal:2021/api/v1/auth/refresh
//		+ X-Forwarded-Proto: https
//		+ X-Forwarded-Host: example.local
//		+ X-Forwarded-For: 203.0.113.77
//
// The CSRF check must see Origin: https://example.local and compute a matching
// server-side origin using the trusted-proxy headers.
func TestSessionRestoreBehindHTTPSProxy(t *testing.T) {
	// The public-facing HTTPS origin
	publicOrigin := "https://example.local"

	// Proxy runs on 172.24.0.2 (Docker bridge network gateway)
	proxyIP := "172.24.0.2"
	trustedProxy := proxyIP + "/32"

	// Configure the auth package with the trusted proxy
	ConfigureProxyTrust([]string{trustedProxy})
	t.Cleanup(func() { ConfigureProxyTrust(nil) })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CSRFOriginCheck(nil, []string{trustedProxy}))

	// Simulate POST /api/v1/auth/refresh
	r.POST("/api/v1/auth/refresh", func(c *gin.Context) {
		// The refresh handler would normally:
		// 1. Read the HttpOnly cookie
		// 2. Validate the refresh token
		// 3. Issue a new access token
		// 4. Rotate the refresh cookie with Secure=true (because scheme is https)

		// Verify the detected scheme
		scheme := RequestScheme(c, currentProxyTrust())
		if scheme != "https" {
			t.Errorf("RequestScheme = %q, want https", scheme)
		}

		// Verify the detected host
		host := RequestHost(c, currentProxyTrust())
		if host != "example.local" {
			t.Errorf("RequestHost = %q, want example.local", host)
		}

		// Verify Secure cookie flag
		secure := requestIsHTTPS(c)
		if !secure {
			t.Error("requestIsHTTPS = false, want true (Secure cookie required)")
		}

		// Set a refresh cookie (simulating rotation)
		SetRefreshSessionCookie(c, "rotation-token", 24*time.Hour)

		c.JSON(http.StatusOK, gin.H{
			"access_token": "new-access-token",
			"token":        "new-access-token",
		})
	})

	// Request arrives from the trusted proxy
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.RemoteAddr = proxyIP + ":54321"

	// Browser sends its Origin (public HTTPS)
	req.Header.Set("Origin", publicOrigin)

	// Trusted proxy sends forwarding headers
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "example.local")
	req.Header.Set("X-Forwarded-For", "203.0.113.77")
	req.Header.Set("Host", "example.local")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// CSRF must accept the request (Origin matches proxy-advertised origin)
	if w.Code != http.StatusOK {
		t.Errorf("refresh rejected with %d: %s", w.Code, w.Body.String())
	}

	// Verify the response includes a Secure cookie
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	cookie := cookies[0]
	if !cookie.Secure {
		t.Error("refresh cookie must have Secure=true under HTTPS")
	}
	if !cookie.HttpOnly {
		t.Error("refresh cookie must be HttpOnly")
	}
	if cookie.SameSite != http.SameSiteLaxMode {
		t.Errorf("refresh cookie SameSite = %v, want Lax", cookie.SameSite)
	}

	// Bootstrap test passed: the browser would receive the new access token
	// and stay on the current page.
}

// TestSessionRestoreBehindIntranetHTTP validates internal HTTP access still
// works when intentionally used (dev labs, internal networks).
func TestSessionRestoreBehindIntranetHTTP(t *testing.T) {
	// Internal HTTP-only access (no TLS, no proxy)
	ConfigureProxyTrust(nil) // No proxies trusted
	t.Cleanup(func() { ConfigureProxyTrust(nil) })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CSRFOriginCheck(nil, nil))

	r.POST("/api/v1/auth/refresh", func(c *gin.Context) {
		// Scheme must be http (no TLS, no trusted proxy)
		scheme := RequestScheme(c, currentProxyTrust())
		if scheme != "http" {
			t.Errorf("RequestScheme = %q, want http", scheme)
		}

		// Secure cookie must be false (plain HTTP)
		if requestIsHTTPS(c) {
			t.Error("requestIsHTTPS = true, want false for plain HTTP")
		}

		SetRefreshSessionCookie(c, "rotation-token", 24*time.Hour)
		c.JSON(http.StatusOK, gin.H{"token": "new-access-token"})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.RemoteAddr = "10.0.0.50:12345"
	// Origin must match the Host for direct HTTP access (no proxy headers)
	req.Header.Set("Origin", "http://10.0.0.50:2021")
	req.Host = "10.0.0.50:2021"

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("plain HTTP refresh rejected: %d", w.Code)
	}

	// Cookie must be non-Secure (plain HTTP deployment)
	if cookies := w.Result().Cookies(); len(cookies) == 1 && cookies[0].Secure {
		t.Error("Secure cookie must NOT be set for plain HTTP")
	}
}

// TestSessionRestoreBlockedWhenProxyIPv6NotTrusted proves that a proxy
// claiming HTTPS on an untrusted address cannot influence the Secure flag
// or CSRF validation.
func TestSessionRestoreBlockedWhenProxyIPv6NotTrusted(t *testing.T) {
	ConfigureProxyTrust([]string{"127.0.0.1/8"}) // Loopback only
	t.Cleanup(func() { ConfigureProxyTrust(nil) })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CSRFOriginCheck(nil, []string{"127.0.0.1/8"}))
	r.POST("/api/v1/auth/refresh", func(c *gin.Context) {
		t.Error("handler must not be reached for untrusted proxy")
	})

	// Proxy at 2001:db8::1 (not trusted)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.RemoteAddr = "[2001:db8::1]:54321"

	// Browser thinks it's HTTPS (because it is, through the untrusted proxy)
	req.Header.Set("Origin", "https://example.local")

	// Untrusted proxy tries to advertise HTTPS
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "example.local")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	// Must reject because the Origin (https://example.local) cannot match
	// the server's internally-computed origin (http://[2001:db8::1]:2021)
	if w.Code != http.StatusForbidden {
		t.Errorf("expected 403 for untrusted IPv6 proxy, got %d", w.Code)
	}
}

// TestNginxProxyManagerCompatibility verifies standard NPM header forwarding.
func TestNginxProxyManagerCompatibility(t *testing.T) {
	proxyIP := "172.24.0.2"
	ConfigureProxyTrust([]string{proxyIP + "/32"})
	t.Cleanup(func() { ConfigureProxyTrust(nil) })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CSRFOriginCheck(nil, []string{proxyIP + "/32"}))

	r.POST("/api/v1/auth/refresh", func(c *gin.Context) {
		// Verify all expected headers are honored
		scheme := RequestScheme(c, currentProxyTrust())
		host := RequestHost(c, currentProxyTrust())

		if scheme != "https" {
			t.Errorf("RequestScheme = %q, want https", scheme)
		}
		if host != "npm-managed.local" {
			t.Errorf("RequestHost = %q, want npm-managed.local", host)
		}

		c.JSON(http.StatusOK, gin.H{"token": "ok"})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.RemoteAddr = proxyIP + ":54321"
	req.Header.Set("Origin", "https://npm-managed.local")

	// Nginx Proxy Manager standard headers:
	// https://nginxproxymanager.com/advanced-config/
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "npm-managed.local")
	req.Header.Set("X-Forwarded-For", "203.0.113.100") // Original client
	req.Header.Set("Host", "npm-managed.local")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("NPM-proxied request rejected: %d", w.Code)
	}
}

// TestZeroTrustTunnelCompatibility covers the tunnel/edge proxy scenario
// where TLS terminates at the edge and internal traffic is plain HTTP.
func TestZeroTrustTunnelCompatibility(t *testing.T) {
	// Tunnel service runs from 10.10.10.1
	tunnelIP := "10.10.10.1"
	ConfigureProxyTrust([]string{tunnelIP + "/32"})
	t.Cleanup(func() { ConfigureProxyTrust(nil) })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(CSRFOriginCheck(nil, []string{tunnelIP + "/32"}))

	r.POST("/api/v1/auth/refresh", func(c *gin.Context) {
		// Even though dydan receives HTTP, the public origin is HTTPS
		scheme := RequestScheme(c, currentProxyTrust())
		if scheme != "https" {
			t.Errorf("RequestScheme = %q, want https (tunnel advertises)", scheme)
		}

		host := RequestHost(c, currentProxyTrust())
		if host != "tunnel.example.com" {
			t.Errorf("RequestHost = %q, want tunnel.example.com", host)
		}

		SetRefreshSessionCookie(c, "tunnel-token", 24*time.Hour)
		c.JSON(http.StatusOK, gin.H{"token": "tunnel-access"})
	})

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/refresh", nil)
	req.RemoteAddr = tunnelIP + ":50000"
	req.Header.Set("Origin", "https://tunnel.example.com")

	// Tunnel/edge proxy sends standard headers
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "tunnel.example.com")
	req.Header.Set("X-Forwarded-For", "198.51.100.50")

	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("tunnel-proxied request rejected: %d", w.Code)
	}

	// Cookie must be Secure (public HTTPS)
	cookies := w.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("expected 1 cookie, got %d", len(cookies))
	}
	if !cookies[0].Secure {
		t.Error("tunnel HTTPS must produce Secure cookie")
	}
}
