package frontend

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alamoudimoh/dydan-server/config"
	"github.com/gin-gonic/gin"
)

func newFrontendRouter(t *testing.T, serve bool) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	r := gin.New()
	Routes(r, NewHandler(&config.Config{Server: config.ServerConfig{ServeFrontend: serve}}))
	return r
}

func get(t *testing.T, r *gin.Engine, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

// TestFrontendSecurityHeaders pins the browser security policy served with the
// embedded frontend: a derived CSP (no unsafe-eval, no wildcard origins,
// explicit object-src 'none') and a referrer policy that keeps URL-attached
// credentials out of third-party Referer headers.
func TestFrontendSecurityHeaders(t *testing.T) {
	r := newFrontendRouter(t, true)

	for _, path := range []string{"/", "/some-spa-route"} {
		w := get(t, r, path)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status %d", path, w.Code)
		}
		csp := w.Header().Get("Content-Security-Policy")
		if csp == "" {
			t.Fatalf("%s: missing Content-Security-Policy", path)
		}
		for _, must := range []string{
			"default-src 'self'",
			"object-src 'none'",
			"base-uri 'self'",
			"frame-ancestors 'self'",
			"form-action 'self'",
		} {
			if !strings.Contains(csp, must) {
				t.Fatalf("%s: CSP missing %q: %s", path, must, csp)
			}
		}
		if strings.Contains(csp, "unsafe-eval") {
			t.Fatalf("%s: CSP must not allow unsafe-eval: %s", path, csp)
		}
		if strings.Contains(csp, "*;") || strings.HasSuffix(csp, "*") {
			t.Fatalf("%s: CSP must not contain wildcard origins: %s", path, csp)
		}
		if got := w.Header().Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
			t.Fatalf("%s: Referrer-Policy = %q", path, got)
		}
	}

	// The SPA fallback (NoRoute) is what actually serves the HTML document in
	// production for client-side routes — it must carry the policy too.
	w := get(t, r, "/client/route/deep")
	if w.Header().Get("Content-Security-Policy") == "" {
		t.Fatal("SPA fallback response missing CSP")
	}
}

// TestAPIResponsesHaveNoCSP documents the intentional scope limit: CSP governs
// browser documents; API JSON responses are not documents and intentionally do
// not carry the policy.
func TestAPIResponsesHaveNoCSP(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/api/v1/resource", func(c *gin.Context) { c.JSON(http.StatusOK, gin.H{"ok": true}) })
	Routes(r, NewHandler(&config.Config{Server: config.ServerConfig{ServeFrontend: false}}))

	w := get(t, r, "/api/v1/resource")
	if w.Header().Get("Content-Security-Policy") != "" {
		t.Fatal("API response must not carry the frontend document CSP")
	}
}

func TestUnknownAPIRouteReturnsJSONNotSPA(t *testing.T) {
	r := newFrontendRouter(t, true)
	w := get(t, r, "/api/v1/not-a-real-route")
	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d", w.Code)
	}
	if contentType := w.Header().Get("Content-Type"); !strings.Contains(contentType, "application/json") {
		t.Fatalf("content type = %q", contentType)
	}
	if !strings.Contains(w.Body.String(), `"code":"api_route_not_found"`) {
		t.Fatalf("unexpected API fallback body: %s", w.Body.String())
	}
}
