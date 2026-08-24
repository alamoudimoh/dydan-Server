package auth

import (
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gin-gonic/gin"
)

// RefreshTokenCookieName is the cookie that carries the browser refresh
// credential. It is always HttpOnly so client-side JavaScript can never read
// it; native clients keep using the JSON body contract instead.
const RefreshTokenCookieName = "refresh_token"

// refreshCookiePath restricts the cookie to API traffic. Older deployments
// stored it at "/", so every clear also expires the legacy path.
const refreshCookiePath = "/api/v1"

const legacyRefreshCookiePath = "/"

// activeProxyTrust holds the deployment's trusted-proxy allowlist. It is
// installed once at startup via ConfigureProxyTrust and read on every
// request; the atomic pointer keeps concurrent reads race-free.
var activeProxyTrust atomic.Pointer[ProxyTrust]

// ConfigureProxyTrust sets the allowlist of proxies whose forwarding headers
// influence security decisions. Must be called during startup with
// cfg.Server.TrustedProxies.
func ConfigureProxyTrust(allowed []string) {
	activeProxyTrust.Store(NewProxyTrust(allowed))
}

func currentProxyTrust() *ProxyTrust {
	return activeProxyTrust.Load()
}

// requestIsHTTPS reports whether the current request arrived over TLS,
// either directly or via an allowlisted reverse proxy. X-Forwarded-Proto from
// any other peer is ignored so directly exposed instances cannot be tricked
// into emitting Secure cookies (or omitting them) based on spoofed headers.
func requestIsHTTPS(c *gin.Context) bool {
	return IsHTTPSRequest(c, currentProxyTrust())
}

// SetRefreshSessionCookie stores the refresh credential as an HttpOnly,
// SameSite=Lax cookie. Secure is derived from the actual request scheme so
// plain-HTTP self-hosted installs keep working while HTTPS deployments
// (direct or behind a proxy) always get a Secure cookie.
func SetRefreshSessionCookie(c *gin.Context, value string, maxAge time.Duration) {
	setRefreshCookie(c, value, int(maxAge.Seconds()))
}

// ClearRefreshSessionCookie expires the refresh cookie on both the current
// and the legacy path so no stale credential survives a logout.
func ClearRefreshSessionCookie(c *gin.Context) {
	setRefreshCookie(c, "", -1)
	setLegacyRefreshCookie(c, "", -1)
}

func setRefreshCookie(c *gin.Context, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     RefreshTokenCookieName,
		Value:    value,
		Path:     refreshCookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   requestIsHTTPS(c),
		SameSite: http.SameSiteLaxMode,
	})
}

func setLegacyRefreshCookie(c *gin.Context, value string, maxAge int) {
	http.SetCookie(c.Writer, &http.Cookie{
		Name:     RefreshTokenCookieName,
		Value:    value,
		Path:     legacyRefreshCookiePath,
		MaxAge:   maxAge,
		HttpOnly: true,
		Secure:   requestIsHTTPS(c),
		SameSite: http.SameSiteLaxMode,
	})
}

// csrfSafeMethod reports whether a method can change server state and thus
// needs origin validation when a cookie-backed credential is in play.
func csrfSafeMethod(method string) bool {
	switch method {
	case http.MethodGet, http.MethodHead, http.MethodOptions:
		return false
	default:
		return true
	}
}

// CSRFOriginCheck rejects state-changing requests whose Origin header points
// at a different site than the one serving the API. This is defense in depth
// for cookie-authenticated endpoints (refresh/logout): SameSite=Lax already
// blocks cross-site POSTs in modern browsers, but older browsers ignore
// SameSite, and CORS alone cannot reject same-site-but-different-port
// attacks. Requests without an Origin header (native apps, curl, server-to-
// server) are allowed. allowedOrigins carries the deployment's configured
// CORS origins for split-origin setups; trustedProxies controls which peers
// may advertise the public scheme/host via forwarding headers.
func CSRFOriginCheck(allowedOrigins []string, trustedProxies []string) gin.HandlerFunc {
	trust := NewProxyTrust(trustedProxies)
	allowed := make(map[string]bool, len(allowedOrigins))
	for _, o := range allowedOrigins {
		allowed[strings.TrimRight(strings.ToLower(o), "/")] = true
	}

	return func(c *gin.Context) {
		if !csrfSafeMethod(c.Request.Method) {
			c.Next()
			return
		}
		raw := c.GetHeader("Origin")
		if raw == "" {
			c.Next()
			return
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" {
			c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Invalid origin"})
			return
		}
		origin := strings.ToLower(parsed.Scheme + "://" + parsed.Host)
		if allowed[origin] {
			c.Next()
			return
		}
		for _, candidate := range requestOriginCandidates(c, trust) {
			if strings.EqualFold(candidate, origin) {
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Cross-origin request rejected"})
	}
}

// requestOriginCandidates enumerates the origins the server itself would use
// for this request, given the already-resolved proxy trust decision.
func requestOriginCandidates(c *gin.Context, trust *ProxyTrust) []string {
	var origins []string
	scheme := RequestScheme(c, trust)
	if host := RequestHost(c, trust); host != "" {
		origins = append(origins, scheme+"://"+host)
	}
	return origins
}
