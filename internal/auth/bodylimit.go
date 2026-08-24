package auth

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"
)

// MaxAuthRequestBodyBytes bounds the JSON body accepted by
// authentication/security-sensitive endpoints (login, signup, password reset,
// refresh, MFA, token creation, OIDC round-trips). These endpoints only carry
// small credential payloads, so an unbounded body is a pure memory-DoS vector:
// a single request could make the server buffer arbitrary data before any
// rate limiter or validator runs. 1 MiB is orders of magnitude above any
// legitimate credential payload and far below the gin multipart default, so
// browser/PWA/native/OIDC flows are unaffected. File uploads are not covered
// by this middleware.
const MaxAuthRequestBodyBytes = 1 << 20 // 1 MiB

// LimitRequestBody caps c.Request.Body at maxBytes and answers 413 once the
// cap is exceeded. Downstream readers (rate-limit key extraction, binding)
// see the capped reader, so the limit holds regardless of how many consumers
// read the body.
func LimitRequestBody(maxBytes int64) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Body == nil {
			c.Next()
			return
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

// IsBodyTooLarge reports whether err is the http.MaxBytesReader overflow.
func IsBodyTooLarge(err error) bool {
	var mbr *http.MaxBytesError
	return errors.As(err, &mbr)
}
