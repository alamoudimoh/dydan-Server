package auth

import (
	"net"
	"strings"

	"github.com/gin-gonic/gin"
)

// ProxyTrust decides whether the client in front of the current request is
// allowed to set forwarding headers (X-Forwarded-Proto, X-Forwarded-Host,
// Forwarded). Dydan is frequently exposed directly to the internet, where
// every such header is attacker-controlled input; it is also commonly run
// behind nginx/Caddy/Traefik on localhost or a private network, where those
// same headers are the only way to see the real scheme and host.
//
// The model is therefore allowlist-based: only requests whose immediate
// remote address falls inside TrustedProxies may influence security-sensitive
// decisions (Secure-cookie detection and CSRF origin derivation). Everyone
// else is treated as a direct client regardless of what headers they send.
type ProxyTrust struct {
	nets []*net.IPNet
}

// NewProxyTrust parses an allowlist of IPs or CIDRs. Entries that are bare
// IPs are converted to /32 (or /128) networks. An empty list trusts nothing,
// which is the safe default for direct exposure.
func NewProxyTrust(allowed []string) *ProxyTrust {
	t := &ProxyTrust{}
	for _, entry := range allowed {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		if !strings.Contains(entry, "/") {
			if ip := net.ParseIP(entry); ip != nil {
				if ip.To4() != nil {
					entry += "/32"
				} else {
					entry += "/128"
				}
			} else {
				continue
			}
		}
		if _, ipnet, err := net.ParseCIDR(entry); err == nil {
			t.nets = append(t.nets, ipnet)
		}
	}
	return t
}

// IsTrusted reports whether the request's immediate peer is an allowlisted
// proxy.
func (t *ProxyTrust) IsTrusted(c *gin.Context) bool {
	if t == nil || len(t.nets) == 0 || c.Request == nil {
		return false
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil {
		host = c.Request.RemoteAddr
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range t.nets {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// forwardedValue returns the first element of a comma-separated forwarding
// header, trimmed. Proxy chains append to these headers, so only the first
// hop's value describes the connection this server accepted.
func forwardedValue(header string) string {
	if header == "" {
		return ""
	}
	if idx := strings.IndexByte(header, ','); idx >= 0 {
		header = header[:idx]
	}
	return strings.TrimSpace(header)
}

// RequestScheme reports the scheme of the connection as the client sees it:
// TLS termination at this process wins; otherwise a trusted proxy may
// advertise https via X-Forwarded-Proto. Anything else is plain http — in
// particular, X-Forwarded-Proto from an untrusted peer is ignored so direct
// clients cannot downgrade/upgrade their own cookie flags.
func RequestScheme(c *gin.Context, trust *ProxyTrust) string {
	if c.Request != nil && c.Request.TLS != nil {
		return "https"
	}
	if trust.IsTrusted(c) && strings.EqualFold(forwardedValue(c.GetHeader("X-Forwarded-Proto")), "https") {
		return "https"
	}
	return "http"
}

// RequestHost reports the public hostname the client used: the Host header
// for direct access, or X-Forwarded-Host when set by a trusted proxy.
func RequestHost(c *gin.Context, trust *ProxyTrust) string {
	if trust.IsTrusted(c) {
		if fh := forwardedValue(c.GetHeader("X-Forwarded-Host")); fh != "" {
			return fh
		}
	}
	if c.Request != nil {
		return c.Request.Host
	}
	return ""
}

// IsHTTPSRequest is the single source of truth for Secure-cookie detection.
func IsHTTPSRequest(c *gin.Context, trust *ProxyTrust) bool {
	return RequestScheme(c, trust) == "https"
}
