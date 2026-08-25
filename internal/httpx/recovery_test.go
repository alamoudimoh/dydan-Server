package httpx

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

const (
	dummyBearer  = "eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxIn0.SflKxwRJSMeKKF2QT4fwpMeJf36POk6yJV_adQssw5c"
	dummyAPITok  = "aaaa0000bbbb1111cccc2222dddd3333eeee4444ffff55556666777788889999"
	dummyRefresh = "refresh-secret-value-0123456789abcdef"
	dummyTicket  = "sse-ticket-value-0123456789abcdef"
)

func panickyRouter(t *testing.T, out *bytes.Buffer) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.DebugMode) // force the request-dump branch
	r := gin.New()
	r.Use(RecoveryWithWriter(out))
	r.GET("/boom", func(c *gin.Context) {
		c.Set("user", nil)
		var u map[string]interface{}
		_ = c.MustGet("user").(map[string]interface{}) //nolint: staticcheck — deliberate panic
		u["trigger"] = true
		c.JSON(http.StatusOK, u)
	})
	return r
}

func credentialRequest(target string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	req.Header.Set("Authorization", "Bearer "+dummyBearer)
	req.Header.Set("secretkey", dummyAPITok)
	req.Header.Set("Cookie", "refresh_token="+dummyRefresh+"; session=xyz")
	req.Header.Set("X-MFA-Code", "123456")
	return req
}

// TestRecoveryNeverDumpsCredentials is the core regression: a panic while a
// fully credentialed request is in flight must not write any secret material
// to diagnostics, in any gin mode.
func TestRecoveryNeverDumpsCredentials(t *testing.T) {
	for _, mode := range []string{gin.DebugMode, gin.ReleaseMode, gin.TestMode} {
		t.Run(mode, func(t *testing.T) {
			gin.SetMode(mode)
			out := &bytes.Buffer{}
			r := gin.New()
			r.Use(RecoveryWithWriter(out))
			r.GET("/boom", func(c *gin.Context) { panic("boom: simulated handler crash") })

			w := httptest.NewRecorder()
			r.ServeHTTP(w, credentialRequest("/boom?ticket="+dummyTicket+"&code=reset-999"))

			if w.Code != http.StatusInternalServerError {
				t.Fatalf("panic must produce 500, got %d", w.Code)
			}
			for name, secret := range map[string]string{
				"access JWT":     dummyBearer,
				"API token":      dummyAPITok,
				"refresh token":  dummyRefresh,
				"SSE ticket":     dummyTicket,
				"MFA code":       "123456",
				"reset code":     "reset-999",
				"session cookie": "session=xyz",
			} {
				if strings.Contains(out.String(), secret) {
					t.Fatalf("%s leaked into panic diagnostics (mode %s): %s", name, mode, out.String())
				}
			}
			if !strings.Contains(out.String(), "simulated handler crash") {
				t.Fatalf("panic value should still be reported for debugging: %q", out.String())
			}
			if mode == gin.DebugMode && !strings.Contains(out.String(), "/boom") {
				t.Fatalf("sanitized dump should keep the request path for debugging")
			}
		})
	}
}

func TestSanitizedRequestDumpRedactsHeadersAndQuery(t *testing.T) {
	req := credentialRequest("/boom?ticket=" + dummyTicket)
	dump := sanitizedRequestDump(req)
	for _, secret := range []string{dummyBearer, dummyAPITok, dummyRefresh, dummyTicket, "123456"} {
		if strings.Contains(dump, secret) {
			t.Fatalf("secret %q present in dump:\n%s", secret, dump)
		}
	}
	for _, want := range []string{"Authorization:", "Secretkey:", "Cookie:", "X-Mfa-Code:", "[redacted]"} {
		if !strings.Contains(dump, want) {
			t.Fatalf("dump missing %q:\n%s", want, dump)
		}
	}
}

func TestRecoveryBrokenPipeDoesNotPanicMiddleware(t *testing.T) {
	gin.SetMode(gin.TestMode)
	out := &bytes.Buffer{}
	r := gin.New()
	r.Use(RecoveryWithWriter(out))
	r.GET("/broken", func(c *gin.Context) {
		panic(newBrokenPipeError())
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/broken", nil))
	// Broken pipe: the connection is dead so no status is written (mirrors
	// gin.Recovery behavior).
	if w.Code != http.StatusOK {
		t.Fatalf("unexpected status %d", w.Code)
	}
	if !strings.Contains(out.String(), "broken pipe") {
		t.Fatalf("broken-pipe dump should be present, got %q", out.String())
	}
	// The dump must not contain credential headers (none were sent here) —
	// assert the header-masking contract explicitly.
	req := credentialRequest("/broken?ticket=abc")
	dump := sanitizedRequestDump(req)
	for _, secret := range []string{dummyBearer, dummyAPITok, dummyRefresh, dummyTicket} {
		if strings.Contains(dump, secret) {
			t.Fatalf("secret %q present in broken-pipe dump:\n%s", secret, dump)
		}
	}
	if !strings.Contains(dump, "ticket=%5Bredacted%5D") {
		t.Fatalf("query ticket should be redacted in dump: %q", dump)
	}
}
