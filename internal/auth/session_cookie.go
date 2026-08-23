package auth

import (
	"net/http"
	"net/url"
	"strings"
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

// requestIsHTTPS reports whether the current request arrived over TLS,
// either directly or via a reverse proxy advertising X-Forwarded-Proto.
func requestIsHTTPS(c *gin.Context) bool {
	if c.Request != nil && c.Request.TLS != nil {
		return true
	}
	return strings.EqualFold(c.GetHeader("X-Forwarded-Proto"), "https")
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

// requestOrigins returns every origin the server itself is willing to serve
// this request from: the proxy-advertised origin first, then Host-based
// fallbacks for direct access.
func requestOrigins(c *gin.Context) []string {
	var origins []string
	scheme := "http"
	if requestIsHTTPS(c) {
		scheme = "https"
	}
	if host := c.GetHeader("X-Forwarded-Host"); host != "" {
		origins = append(origins, scheme+"://"+host)
	}
	if c.Request != nil && c.Request.Host != "" {
		origins = append(origins, scheme+"://"+c.Request.Host)
	}
	return origins
}

// CSRFOriginCheck rejects state-changing requests whose Origin header points
// at a different site than the one serving the API. This is defense in depth
// for cookie-authenticated endpoints (refresh/logout): SameSite=Lax already
// blocks cross-site POSTs in modern browsers, but older browsers ignore
// SameSite, and CORS alone cannot reject same-site-but-different-port
// attacks. Requests without an Origin header (native apps, curl, server-to-
// server) are allowed. allowedOrigins carries the deployment's configured
// CORS origins for split-origin setups.
func CSRFOriginCheck(allowedOrigins []string) gin.HandlerFunc {
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
		for _, candidate := range requestOrigins(c) {
			if strings.EqualFold(candidate, origin) {
				c.Next()
				return
			}
		}
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "Cross-origin request rejected"})
	}
}
