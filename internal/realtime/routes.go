package realtime

import (
	"github.com/alamoudimoh/dydan-server/config"
	"github.com/alamoudimoh/dydan-server/internal/authlimit"
	ginJWT "github.com/appleboy/gin-jwt/v2"
	"github.com/gin-gonic/gin"
)

// Routes sets up the real-time service routes. The auth middleware and polling
// handler are provided by fx (see main.go) so their lifecycle and shared
// dependencies (e.g. the ticket store) are managed in one place.
func Routes(
	router *gin.Engine,
	rtAuthMiddleware *AuthMiddleware,
	pollingHandler *PollingHandler,
	jwtAuth *ginJWT.GinJWTMiddleware,
	limiter *authlimit.Limiter,
	cfg *config.Config,
) {
	// Real-time API group
	rtGroup := router.Group("/api/v1/realtime")

	// SSE endpoint (with auth)
	rtGroup.GET("/sse", rtAuthMiddleware.WebSocketAuthHandler(), pollingHandler.HandleSSE)

	// SSE ticket endpoint (header-authenticated). Native EventSource clients that
	// cannot send custom headers call this first, then open /sse?ticket=...
	// JWT validation runs BEFORE the abuse limiter so minting is keyed by both
	// IP and authenticated user (a NAT'd household cannot exhaust one shared
	// bucket). Unauthenticated requests fail at the JWT layer without touching
	// the limiter datastore. The DB-backed limiter bounds minting
	// replica-independently, so flooding cannot exhaust the TicketStore.
	rtGroup.GET("/sse/ticket",
		jwtAuth.MiddlewareFunc(),
		limiter.Middleware("sse-ticket", cfg.Server.RateLimitAuthPeriod, int64(cfg.Server.RateLimitAuth)),
		pollingHandler.HandleSSETicket)

	// Admin endpoints (require authentication)
	adminGroup := rtGroup.Group("/admin")
	adminGroup.Use(jwtAuth.MiddlewareFunc())
	{
		// adminGroup.GET("/stats", wsHandler.HandleHealthCheck)
		// adminGroup.GET("/connections/:circleId", wsHandler.HandleConnectionStats)
	}
}
