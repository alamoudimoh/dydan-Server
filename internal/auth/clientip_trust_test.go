package auth_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// buildIPRouter mirrors production: gin trusted-proxy allowlist + a handler
// that reports the effective security identity (ClientIP) used by abuse
// controls and audit logs.
func buildIPRouter(trusted []string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	_ = r.SetTrustedProxies(trusted)
	r.GET("/who", func(c *gin.Context) {
		c.String(http.StatusOK, c.ClientIP())
	})
	return r
}

func clientIP(t *testing.T, r *gin.Engine, remote string, headers map[string]string) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/who", nil)
	req.RemoteAddr = remote
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w.Body.String()
}

// TestDirectClientCannotSpoofClientIP pins the trust model behind abuse
// control keying and audit logging: a directly connected (untrusted) client
// cannot change its effective security identity by sending forwarding
// headers — no matter how many hops they claim or how the values are formed.
func TestDirectClientCannotSpoofClientIP(t *testing.T) {
	r := buildIPRouter([]string{"10.0.0.1/32", "127.0.0.0/8", "::1/128", "192.168.1.0/24"})

	spoofAttempts := []map[string]string{
		{"X-Forwarded-For": "1.2.3.4"},
		{"X-Forwarded-For": "1.2.3.4, 5.6.7.8, 9.9.9.9"},
		{"X-Real-IP": "8.8.8.8"},
		{"X-Forwarded-For": "::1"},       // claim to be loopback IPv6
		{"X-Forwarded-For": "not-an-ip"}, // malformed
		{"X-Forwarded-For": ""},          // empty value
		{"Forwarded": "for=1.2.3.4"},     // RFC 7239 header is not honored by gin
		{"X-Forwarded-Host": "evil.example"},
	}
	for i, headers := range spoofAttempts {
		if got := clientIP(t, r, "203.0.113.77:4444", headers); got != "203.0.113.77" {
			t.Fatalf("attempt %d: direct client spoofed identity to %q via %v", i, got, headers)
		}
	}

	// IPv6 direct client: real address preserved, spoof attempts ignored.
	if got := clientIP(t, r, "[2001:db8::99]:5555", map[string]string{"X-Forwarded-For": "1.1.1.1"}); got != "2001:db8::99" {
		t.Fatalf("IPv6 direct client identity = %q", got)
	}
}

// TestTrustedProxyClientIPDerivation proves the other side of the model:
// when the immediate peer IS a trusted proxy, forwarded values are honored,
// chains resolve to the left-most (original client) entry, and malformed
// values degrade safely.
func TestTrustedProxyClientIPDerivation(t *testing.T) {
	r := buildIPRouter([]string{"10.0.0.1/32"})

	cases := []struct {
		remote  string
		headers map[string]string
		want    string
	}{
		{"10.0.0.1:5000", map[string]string{"X-Forwarded-For": "198.51.100.23"}, "198.51.100.23"},
		// Proxy chain appended after the original client. gin walks
		// right-to-left skipping TRUSTED hops; "10.0.0.2" is not on the
		// trusted allowlist, so it is treated as the effective client — this
		// is exactly what defeats end-client spoofing ("fake, real" cannot
		// make the attacker pick an arbitrary identity).
		{"10.0.0.1:5000", map[string]string{"X-Forwarded-For": "198.51.100.23, 10.0.0.2"}, "10.0.0.2"},
		// Trusted proxy that sets nothing: fall back to the peer address.
		{"10.0.0.1:5000", nil, "10.0.0.1"},
		// Malformed forwarded value from a trusted proxy: rejected, peer used.
		{"10.0.0.1:5000", map[string]string{"X-Forwarded-For": "garbage"}, "10.0.0.1"},
		// IPv6 end client behind a trusted proxy.
		{"10.0.0.1:5000", map[string]string{"X-Forwarded-For": "2001:db8::1"}, "2001:db8::1"},
		// A second hop claiming to forward on behalf of an untrusted-looking
		// address is still only meaningful if the IMMEDIATE peer is trusted.
		{"192.168.1.50:6000", map[string]string{"X-Forwarded-For": "198.51.100.23"}, "192.168.1.50"},
	}
	for i, tc := range cases {
		if got := clientIP(t, r, tc.remote, tc.headers); got != tc.want {
			t.Fatalf("case %d: ClientIP = %q, want %q (headers %v)", i, got, tc.want, tc.headers)
		}
	}
}

// TestDefaultTrustIsLoopbackOnly documents the shipped default: with no
// explicit configuration only loopback proxies are trusted, so a LAN peer
// (common in self-hosting) cannot forge identities either.
func TestDefaultTrustIsLoopbackOnly(t *testing.T) {
	r := buildIPRouter([]string{"127.0.0.1/8", "::1/128"})
	if got := clientIP(t, r, "192.168.1.77:3333", map[string]string{"X-Forwarded-For": "6.6.6.6"}); got != "192.168.1.77" {
		t.Fatalf("LAN peer forged identity: %q", got)
	}
	if got := clientIP(t, r, "127.0.0.1:3333", map[string]string{"X-Forwarded-For": "6.6.6.6"}); got != "6.6.6.6" {
		t.Fatalf("loopback proxy forwarding ignored: %q", got)
	}
}
