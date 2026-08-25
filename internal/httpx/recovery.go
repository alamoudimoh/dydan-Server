// Package httpx holds small shared HTTP middleware helpers.
package httpx

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// sensitiveHeaders are masked in any panic-time request dump. gin's built-in
// Recovery only masks "Authorization"; DayDan also authenticates with the
// "secretkey" API-token header, browser sessions ride on Cookie headers, the
// refresh token can appear as a "refresh_token" header, and MFA codes travel
// in "X-MFA-Code". A panic in debug mode would otherwise write all of them
// verbatim to stderr.
var sensitiveHeaders = []string{
	"Authorization",
	"Cookie",
	"Secretkey",
	"Refresh-Token",
	"X-Mfa-Code",
}

const redacted = "[redacted]"

// Recovery replaces gin.Recovery() so that panics can never leak credentials.
// Behavior mirrors gin.Recovery (broken-pipe handling, 500 response, stack
// trace) except that any request dump is sanitized: credential headers are
// masked and the URL query string is replaced, because query strings may
// carry single-use credentials (SSE tickets, legacy password-reset links).
func Recovery() gin.HandlerFunc {
	return RecoveryWithWriter(os.Stderr)
}

// RecoveryWithWriter lets callers capture diagnostic output instead of
// writing to stderr (used by tests).
func RecoveryWithWriter(out io.Writer) gin.HandlerFunc {
	var logger *log.Logger
	if out != nil {
		logger = log.New(out, "\n\n\x1b[31m", log.LstdFlags)
	}
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				// Mirror gin.Recovery: a broken client connection is not a
				// condition that warrants a full panic trace.
				var err error
				switch e := rec.(type) {
				case error:
					err = e
				case string:
					err = errors.New(e)
				default:
					err = fmt.Errorf("%v", e)
				}
				var brokenPipe bool
				var opErr *net.OpError
				if errors.As(err, &opErr) {
					var se *os.SyscallError
					if errors.As(opErr, &se) {
						seStr := strings.ToLower(se.Error())
						if strings.Contains(seStr, "broken pipe") ||
							strings.Contains(seStr, "connection reset by peer") {
							brokenPipe = true
						}
					}
				}
				if logger != nil {
					stack := make([]byte, 8192)
					stack = stack[:runtime.Stack(stack, false)]
					switch {
					case brokenPipe:
						logger.Printf("%s\n%s", err, sanitizedRequestDump(c.Request))
					case gin.IsDebugging():
						logger.Printf("[Recovery] %s panic recovered:\n%s\n%s\n%s",
							time.Now().UTC().Format(time.RFC3339),
							sanitizedRequestDump(c.Request), err, stack)
					default:
						logger.Printf("[Recovery] %s panic recovered:\n%s\n%s",
							time.Now().UTC().Format(time.RFC3339), err, stack)
					}
				}
				if brokenPipe {
					// If the connection is dead we cannot write a status.
					_ = c.Error(err)
					c.Abort()
				} else {
					c.AbortWithStatus(http.StatusInternalServerError)
				}
			}
		}()
		c.Next()
	}
}

// sanitizedRequestDump renders the request with credential headers masked and
// sensitive query-parameter values redacted (path and harmless query
// parameters are preserved for debugging). Defensive against nil URL: this
// runs inside a panic handler and must never panic itself.
func sanitizedRequestDump(r *http.Request) string {
	dumpCopy := r.Clone(context.Background())
	if dumpCopy.URL == nil {
		dumpCopy.URL = &url.URL{}
	}
	dumpCopy.RequestURI = RedactURL(dumpCopy.URL)
	// DumpRequest renders the request line from RequestURI, so strip userinfo
	// and re-derive the query from the already-redacted form.
	dumpCopy.URL.RawQuery = ""
	dumpCopy.URL.User = nil
	httpRequest, _ := httputil.DumpRequest(dumpCopy, false)
	lines := strings.Split(string(httpRequest), "\r\n")
	for i, line := range lines {
		colon := strings.IndexByte(line, ':')
		if colon <= 0 {
			continue
		}
		name := line[:colon]
		for _, sensitive := range sensitiveHeaders {
			if strings.EqualFold(name, sensitive) {
				lines[i] = name + ": " + redacted
			}
		}
	}
	return strings.Join(lines, "\r\n")
}
