package frontend

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"

	"github.com/alamoudimoh/dydan-server/config"
	"github.com/gin-gonic/gin"
)

//go:embed dist
var embeddedFiles embed.FS

// contentSecurityPolicy is derived from an inventory of what the production
// DayDan frontend actually loads (verified against a real `vite build` of the
// daydan-app bundle):
//
//   - default-src 'self'            — everything same-origin unless opened below;
//   - script-src 'self' plus Google Identity Services, Sign in with Apple,
//     and the Facebook SDK hosts that the login page injects at runtime
//     (accounts.google.com/gsi/client, appleid.cdn-apple.com/appleauth,
//     connect.facebook.net/sdk.js). Without these hosts social/OIDC login
//     would break. No 'unsafe-inline'/'unsafe-eval' is required because the
//     built index.html contains only external module scripts;
//   - style-src 'self' 'unsafe-inline'
//     — REQUIRED and documented: several lazy chunks create <style> elements
//     at runtime (dynamic theming), so blocking inline styles would visually
//     break the app. This is the strongest style policy the current build
//     permits without a frontend rewrite;
//   - img-src 'self' data: blob:    — bundled icons plus runtime object URLs
//     for attachment previews;
//   - font-src 'self' data:         — same-origin woff2 files and base64
//     data-fonts inlined by Tailwind's font stack;
//   - connect-src 'self'            — API and SSE are same-origin because this
//     server serves the frontend itself; split-origin deployments serve the
//     frontend elsewhere and do not get this header at all;
//   - frame-src for the Google/Apple/Facebook hosted sign-in frames;
//   - worker-src 'self'             — PWA service worker (same-origin sw.js);
//   - manifest-src 'self'; object-src 'none'; base-uri 'self';
//     frame-ancestors 'self' (clickjacking guard); form-action 'self'.
const contentSecurityPolicy = "default-src 'self'; " +
	"script-src 'self' https://accounts.google.com https://appleid.cdn-apple.com https://connect.facebook.net; " +
	"style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: blob:; " +
	"font-src 'self' data:; " +
	"connect-src 'self'; " +
	"frame-src https://accounts.google.com https://appleid.cdn-apple.com https://www.facebook.com; " +
	"worker-src 'self'; " +
	"manifest-src 'self'; " +
	"object-src 'none'; " +
	"base-uri 'self'; " +
	"frame-ancestors 'self'; " +
	"form-action 'self'"

// referrerPolicy keeps URL-attached credentials (legacy ?c= reset links,
// realtime tickets) from leaking to third parties via the Referer header.
const referrerPolicy = "strict-origin-when-cross-origin"

func setBrowserSecurityHeaders(c *gin.Context) {
	c.Header("Content-Security-Policy", contentSecurityPolicy)
	c.Header("Referrer-Policy", referrerPolicy)
}

type Handler struct {
	ServeFrontend bool
}

func NewHandler(config *config.Config) *Handler {
	return &Handler{
		ServeFrontend: config.Server.ServeFrontend,
	}
}

func Routes(router *gin.Engine, h *Handler) {
	// this whole logic is walkaround for serving frontend files
	// TODO: figure out better way to improve it. main issue i run into is failing over to index.html when file does not exist

	if h.ServeFrontend {
		// if file exists in dist folder, serve it
		router.Use(staticMiddleware("dist"))
		// if file does not exist in dist folder fallback to index.html
		router.NoRoute(staticMiddlewareNoRoute("dist"))

	}

}

func staticMiddleware(root string) gin.HandlerFunc {
	fileServer := http.FileServer(getFileSystem(root))

	return func(c *gin.Context) {
		_, err := fs.Stat(embeddedFiles, "dist"+c.Request.URL.Path)
		if err != nil {
			c.Next()
			return
		}
		// Cache-busted assets (filenames contain content hashes) can be
		// cached indefinitely. Other files like index.html must not be
		// cached so that updates are picked up immediately.
		if strings.HasPrefix(c.Request.URL.Path, "/assets/") {
			c.Header("Cache-Control", "public, max-age=31536000, immutable")
		}
		setBrowserSecurityHeaders(c)
		fileServer.ServeHTTP(c.Writer, c.Request)

	}
}
func staticMiddlewareNoRoute(root string) gin.HandlerFunc {
	fileServer := http.FileServer(getFileSystem(root))

	// always serve index.html for any route does not match:
	return func(c *gin.Context) {
		// Rewrite all requests to serve index.html
		c.Request.URL.Path = "/"
		setBrowserSecurityHeaders(c)
		fileServer.ServeHTTP(c.Writer, c.Request)

	}
}

func getFileSystem(path string) http.FileSystem {
	fs, err := fs.Sub(embeddedFiles, path)
	if err != nil {
		panic(err)
	}
	return http.FS(fs)
}
