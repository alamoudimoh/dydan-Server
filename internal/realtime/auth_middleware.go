package realtime

import (
	"context"
	"net/http"
	"strings"

	"github.com/alamoudimoh/daydan-server/config"
	daydanAuth "github.com/alamoudimoh/daydan-server/internal/auth"
	cRepo "github.com/alamoudimoh/daydan-server/internal/circle/repo"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	"github.com/alamoudimoh/daydan-server/logging"
	ginJWT "github.com/appleboy/gin-jwt/v2"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v4"
)

// AuthMiddleware handles WebSocket authentication
type AuthMiddleware struct {
	userRepo    *uRepo.UserRepository
	circleRepo  *cRepo.CircleRepository
	jwtAuth     *ginJWT.GinJWTMiddleware
	config      *config.Config
	ticketStore *TicketStore
}

// NewAuthMiddleware creates a new WebSocket auth middleware
func NewAuthMiddleware(
	userRepo *uRepo.UserRepository,
	circleRepo *cRepo.CircleRepository,
	jwtAuth *ginJWT.GinJWTMiddleware,
	config *config.Config,
	ticketStore *TicketStore,
) *AuthMiddleware {
	return &AuthMiddleware{
		userRepo:    userRepo,
		circleRepo:  circleRepo,
		jwtAuth:     jwtAuth,
		config:      config,
		ticketStore: ticketStore,
	}
}

// AuthenticateConnection authenticates a WebSocket connection request
func (am *AuthMiddleware) AuthenticateConnection(c *gin.Context) (*uModel.User, int, error) {
	logger := logging.FromContext(c.Request.Context())

	// Native EventSource clients (e.g. the iOS/Android Capacitor app) cannot send
	// a custom Authorization header, so they authenticate with a short-lived,
	// single-use ticket passed as a query parameter. Prefer the ticket when
	// present so we never fall through to JWT validation for it.
	if ticket := c.Query("ticket"); ticket != "" {
		securityState, ok := am.ticketStore.Consume(ticket)
		if !ok {
			return nil, 0, ErrInvalidToken
		}

		user, err := daydanAuth.ValidateAccessClaims(c.Request.Context(), am.userRepo, securityState.claims)
		if err != nil || user.ID != securityState.userID {
			logger.Errorw("SSE ticket security state is no longer valid", "error", err)
			return nil, 0, ErrInvalidToken
		}
		c.Set("accessSecurityClaims", securityState.claims)

		return &user.User, user.CircleID, nil
	}

	// Extract token from query parameter or header
	token := am.extractToken(c)
	if token == "" {
		return nil, 0, ErrMissingToken
	}

	// Validate JWT token
	claims, err := am.validateToken(token)
	if err != nil {
		if err.Error() == "Token is expired" {
			c.Status(427)
			c.Abort()
			return nil, 0, ErrTokenExpired
		}

		logger.Errorw("Invalid token for WebSocket connection", "error", err)
		return nil, 0, ErrInvalidToken
	}

	user, err := daydanAuth.ValidateAccessClaims(c.Request.Context(), am.userRepo, map[string]interface{}(claims))
	if err != nil {
		logger.Errorw("JWT security state is no longer valid", "error", err)
		return nil, 0, ErrInvalidToken
	}
	c.Set("accessSecurityClaims", map[string]interface{}(claims))

	return &user.User, user.CircleID, nil
}

// extractToken extracts the JWT token from the request
func (am *AuthMiddleware) extractToken(c *gin.Context) string {
	// Try Authorization header first (more secure)
	authHeader := c.GetHeader("Authorization")
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 && parts[0] == "Bearer" {
			return parts[1]
		}
	}
	// see the cookies for the token
	if cookie, err := c.Cookie(am.jwtAuth.CookieName); err == nil && cookie != "" {
		return cookie
	}

	// Try WebSocket subprotocol for token (more secure than query params)
	if wsProtocols := c.Request.Header.Get("Sec-WebSocket-Protocol"); wsProtocols != "" {
		protocols := strings.Split(wsProtocols, ",")
		for _, protocol := range protocols {
			protocol = strings.TrimSpace(protocol)
			if strings.HasPrefix(protocol, "access_token.") {
				return strings.TrimPrefix(protocol, "access_token.")
			}
		}
	}

	return ""
}

// validateToken validates the JWT token and returns claims
func (am *AuthMiddleware) validateToken(tokenString string) (jwt.MapClaims, error) {
	// Parse the token using the same secret as the main JWT middleware
	token, err := jwt.Parse(tokenString, func(token *jwt.Token) (interface{}, error) {
		// Validate the signing method
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, ErrInvalidToken
		}
		// Return the secret key used by the main JWT middleware
		return []byte(am.config.Jwt.Secret), nil
	})

	if err != nil {
		return nil, err
	}

	if !token.Valid {
		return nil, ErrInvalidToken
	}

	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return nil, ErrInvalidToken
	}

	return claims, nil
}

// CheckCircleAccess verifies if a user has access to a specific circle
func (am *AuthMiddleware) CheckCircleAccess(ctx context.Context, userID, circleID int) error {
	user, err := am.userRepo.GetUserByID(ctx, userID)
	if err != nil {
		return err
	}

	if user.CircleID != circleID {
		return ErrUnauthorizedCircle
	}

	return nil
}

// WebSocketAuthHandler creates a Gin handler for WebSocket authentication
func (am *AuthMiddleware) WebSocketAuthHandler() gin.HandlerFunc {
	return func(c *gin.Context) {
		logger := logging.FromContext(c.Request.Context())
		user, circleID, err := am.AuthenticateConnection(c)
		if err != nil {
			logger.Warnw("WebSocket authentication failed",
				"error", err,
				"remote_addr", c.ClientIP())

			c.JSON(http.StatusUnauthorized, gin.H{
				"error": "Authentication failed",
				"code":  "AUTH_FAILED",
			})
			c.Abort()
			return
		}
		if !user.IsPlusMember() {
			logger.Warnw("WebSocket access denied for non-plus member",
				"user_id", user.ID,
				"remote_addr", c.ClientIP())

			c.JSON(http.StatusForbidden, gin.H{
				"error": "Access denied for non-plus members",
				"code":  "ACCESS_DENIED",
			})
			c.Abort()
			return
		}

		// Store user and circle ID in context for the WebSocket handler
		c.Set("user", user)
		c.Set("circleId", circleID)
		c.Next()
	}
}
