package resource

import (
	"context"
	"net/http"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	jwt "github.com/appleboy/gin-jwt/v2"
	"github.com/gin-gonic/gin"
	"github.com/ulule/limiter/v3"
	"gorm.io/gorm"
)

type Resource struct {
	Idp                    identityProvider `json:"identity_provider" binding:"omitempty"`
	MinVersion             string           `json:"min_version" binding:"omitempty"`
	APIVersion             string           `json:"api_version" binding:"omitempty"`
	APICommit              string           `json:"api_commit" binding:"omitempty"`
	IsUserCreationDisabled bool             `json:"is_user_creation_disabled"`
	SingleCircleInstance   bool             `json:"single_circle_instance"`
	// DisablePasswordAuth tells the client to hide username/password login and
	// signup, leaving only SSO (#438).
	DisablePasswordAuth bool `json:"disable_password_auth"`
}
type identityProvider struct {
	Auth_url  string `json:"auth_url" binding:"omitempty"`
	Client_ID string `json:"client_id" binding:"omitempty"`
	Name      string `json:"name" binding:"omitempty"`
}

type Handler struct {
	config config.Config
	db     *gorm.DB
}

func NewHandler(cfg *config.Config, db *gorm.DB) *Handler {
	return &Handler{
		config: *cfg,
		db:     db,
	}
}

func (h *Handler) getResource(c *gin.Context) {
	c.JSON(200, &Resource{
		Idp: identityProvider{
			Auth_url:  h.config.OAuth2Config.AuthURL,
			Client_ID: h.config.OAuth2Config.ClientID,
			Name:      h.config.OAuth2Config.Name,
		},
		MinVersion:             h.config.MinVersion,
		APIVersion:             h.config.Info.Version,
		APICommit:              h.config.Info.Commit,
		IsUserCreationDisabled: h.config.IsUserCreationDisabled,
		SingleCircleInstance:   h.config.SingleCircleInstance,
		DisablePasswordAuth:    h.config.DisablePasswordAuth,
	})
}

func (h *Handler) getHealth(c *gin.Context) {
	c.JSON(200, gin.H{
		"status":  "healthy",
		"version": h.config.Info.Version,
		"commit":  h.config.Info.Commit,
	})
}

// getDBHealth is the database-backed health check. Beyond liveness and DB
// reachability it reports the API-token migration state as a count/status
// only: how many legacy plaintext credential rows remain after the
// 20260824_hash_api_token_secrets sweep. Operators can therefore tell a
// fully drained installation ("complete", 0 rows) from one where some legacy
// tokens are still awaiting lazy upgrade ("pending"). No token values, no
// digests, no per-token detail is ever included; the field stays constant
// shape so dashboards can scrape it without high-cardinality dimensions.
func (h *Handler) getDBHealth(c *gin.Context) {
	pingCtx, cancel := context.WithTimeout(c.Request.Context(), 3*time.Second)
	defer cancel()

	sqlDB, err := h.db.DB()
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "error": "database unavailable"})
		return
	}
	if err := sqlDB.PingContext(pingCtx); err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unhealthy", "error": "database unreachable"})
		return
	}

	legacyRows, err := uRepo.CountLegacyPlaintextAPITokens(c.Request.Context(), h.db)
	tokenMigration := gin.H{}
	switch {
	case err != nil:
		// The count is diagnostic, not a health signal: never fail the
		// probe because observability could not be computed.
		tokenMigration["state"] = "unknown"
	default:
		tokenMigration["legacy_plaintext_rows"] = legacyRows
		if legacyRows == 0 {
			tokenMigration["state"] = "complete"
		} else {
			tokenMigration["state"] = "pending"
		}
	}

	c.JSON(http.StatusOK, gin.H{
		"status":              "ok",
		"api_token_migration": tokenMigration,
	})
}

func Routes(r *gin.Engine, h *Handler, auth *jwt.GinJWTMiddleware, limiter *limiter.Limiter) {
	// No-auth health check endpoint for container/orchestrator health checks.
	r.GET("/health", h.getHealth)
	// Database-backed health check used by orchestrators that want to detect
	// a wedged DB, plus the API-token plaintext-migration status counter.
	r.GET("/api/v1/health", h.getDBHealth)

	resourceRoutes := r.Group("api/v1/resource")

	resourceRoutes.GET("", h.getResource)

}
