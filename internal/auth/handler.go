package auth

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/alamoudimoh/daydan-server/internal/mfa"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	"github.com/alamoudimoh/daydan-server/logging"
	jwt "github.com/appleboy/gin-jwt/v2"
	"github.com/gin-gonic/gin"
)

// AuthHandler handles authentication endpoints
type AuthHandler struct {
	tokenService *TokenService
	userRepo     *uRepo.UserRepository
	jwtAuth      *jwt.GinJWTMiddleware
	mfaService   *mfa.MFAService
	csrfCheck    gin.HandlerFunc
}

// NewAuthHandler creates a new auth handler
func NewAuthHandler(tokenService *TokenService, userRepo *uRepo.UserRepository, jwtAuth *jwt.GinJWTMiddleware, mfaService *mfa.MFAService) *AuthHandler {
	return &AuthHandler{
		tokenService: tokenService,
		userRepo:     userRepo,
		jwtAuth:      jwtAuth,
		mfaService:   mfaService,
	}
}

// SetCSRFCheck attaches origin validation for cookie-authenticated endpoints.
// It is optional so existing constructors keep working; handlers fall back to
// a permissive no-op when it is not configured (tests).
func (h *AuthHandler) SetCSRFCheck(check gin.HandlerFunc) {
	h.csrfCheck = check
}

func (h *AuthHandler) requireCSRF(c *gin.Context) bool {
	if h.csrfCheck == nil {
		return true
	}
	h.csrfCheck(c)
	return !c.IsAborted()
}

// RefreshTokenHandler handles POST /auth/refresh
func (h *AuthHandler) RefreshTokenHandler(c *gin.Context) {
	// Cookie-authenticated endpoint: validate the Origin before doing work so
	// cross-site requests cannot ride the refresh credential.
	if !h.requireCSRF(c) {
		return
	}
	// Try to get refresh token from cookie first (httpOnly), fallback to body (legacy)
	refreshToken, err := c.Cookie(RefreshTokenCookieName)
	if err != nil {
		// Fallback to JSON body for backward compatibility
		var req RefreshRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "Invalid request: " + err.Error(),
			})
			return
		}
		refreshToken = req.RefreshToken
	}

	if refreshToken == "" {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Refresh token required",
		})
		return
	}

	// Validate and refresh tokens
	tokenResponse, err := h.tokenService.RefreshTokens(c.Request.Context(), refreshToken)
	if err != nil {
		logging.FromContext(c).Errorw("Failed to refresh token", "error", err)
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "Invalid or expired refresh token",
			"code":  "INVALID_REFRESH_TOKEN",
		})
		return
	}

	// Set new refresh token as httpOnly cookie
	SetRefreshSessionCookie(c, tokenResponse.RefreshToken, h.tokenService.refreshTokenExpiry)

	c.JSON(http.StatusOK, tokenResponse)
}

// LogoutHandler handles POST /auth/logout
func (h *AuthHandler) LogoutHandler(c *gin.Context) {
	// Cookie-authenticated endpoint: validate the Origin before doing work.
	if !h.requireCSRF(c) {
		return
	}
	refreshToken, err := c.Cookie(RefreshTokenCookieName)
	if err != nil {
		// Fallback to JSON body for backward compatibility
		var req RefreshRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			// No token provided, but logout should still succeed
			refreshToken = ""
		} else {
			refreshToken = req.RefreshToken
		}
	}

	// Revoke the refresh token if we have one
	if refreshToken != "" {
		if err := h.tokenService.RevokeToken(c.Request.Context(), refreshToken); err != nil {
			logging.FromContext(c).Errorw("Failed to revoke token", "error", err)
			// Don't return error - logout should always succeed from client perspective
		}
	}

	// Clear the httpOnly cookie (both current and legacy paths)
	ClearRefreshSessionCookie(c)

	c.JSON(http.StatusOK, gin.H{
		"message": "Logged out successfully",
	})
}

// RevokeAllHandler handles POST /auth/revoke-all
func (h *AuthHandler) RevokeAllHandler(c *gin.Context) {
	// Get current user from JWT token
	currentUser, ok := CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "Unauthorized",
		})
		return
	}

	// Revoke all sessions for the user
	if err := h.tokenService.RevokeAllUserTokens(c.Request.Context(), currentUser.ID); err != nil {
		logging.FromContext(c).Errorw("Failed to revoke all tokens", "error", err, "userID", currentUser.ID)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to revoke sessions",
		})
		return
	}

	// Clear the httpOnly cookie since all sessions are revoked
	ClearRefreshSessionCookie(c)

	c.JSON(http.StatusOK, gin.H{
		"message": "All sessions revoked successfully",
	})
}

// EnhancedLoginHandler provides enhanced login with refresh tokens
func (h *AuthHandler) EnhancedLoginHandler(c *gin.Context) {
	logger := logging.FromContext(c)
	var loginReq struct {
		Username string `form:"username" json:"username" binding:"required"`
		Password string `form:"password" json:"password" binding:"required"`
	}

	if err := c.ShouldBindJSON(&loginReq); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": "Invalid request: " + err.Error(),
		})
		return
	}

	// Authenticate using the existing authenticator logic
	userDetails, err := h.authenticateUser(c, loginReq.Username, loginReq.Password)
	if err != nil {
		c.JSON(http.StatusUnauthorized, gin.H{
			"error": "Invalid credentials",
		})
		return
	}

	// Check if user has MFA enabled
	if userDetails.MFAEnabled {
		sessionToken, err := h.mfaService.GenerateSessionToken()
		if err != nil {
			logger.Errorw("Failed to generate MFA session token", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication failed"})
			return
		}

		mfaSession := &uModel.MFASession{
			SessionToken: sessionToken,
			UserID:       userDetails.ID,
			AuthMethod:   "password",
			Verified:     false,
			CreatedAt:    time.Now().UTC(),
			ExpiresAt:    time.Now().UTC().Add(10 * time.Minute),
			UserData:     userDetails.Username,
		}

		if err := h.userRepo.CreateMFASession(c, mfaSession); err != nil {
			logger.Errorw("Failed to create MFA session", "error", err)
			c.JSON(http.StatusInternalServerError, gin.H{"error": "Authentication failed"})
			return
		}

		c.JSON(http.StatusOK, gin.H{
			"mfaRequired":  true,
			"sessionToken": sessionToken,
		})
		return
	}

	// Generate both access and refresh tokens
	tokenResponse, err := h.tokenService.GenerateTokens(c.Request.Context(), userDetails)
	if err != nil {
		logger.Errorw("Failed to generate tokens", "error", err)
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": "Failed to generate tokens",
		})
		return
	}
	SetRefreshSessionCookie(c, tokenResponse.RefreshToken, h.tokenService.refreshTokenExpiry)
	c.JSON(http.StatusOK, tokenResponse)
}

// authenticateUser performs user authentication (extracted from JWT middleware)
func (h *AuthHandler) authenticateUser(c *gin.Context, username, password string) (*uModel.UserDetails, error) {
	user, err := h.userRepo.GetUserByUsername(c.Request.Context(), username)
	if err != nil || user.Disabled {
		return nil, fmt.Errorf("user not found or disabled")
	}

	err = Matches(user.Password, password)
	if err != nil {
		return nil, fmt.Errorf("invalid password")
	}

	return user, nil
}

// Enhanced login response that uses token service
func (h *AuthHandler) EnhancedLoginResponse(c *gin.Context, code int, token string, expire time.Time, user *uModel.UserDetails) {
	// Generate refresh token along with access token
	tokenResponse, err := h.tokenService.GenerateTokens(context.TODO(), user)
	if err != nil {
		logging.FromContext(c).Errorw("Failed to generate refresh token", "error", err)
		// Fallback to original response
		c.JSON(code, gin.H{
			"code":   code,
			"token":  token,
			"expire": expire,
		})
		return
	}

	c.JSON(code, tokenResponse)
}

// CleanupExpiredSessions runs periodic cleanup of expired sessions
func (h *AuthHandler) CleanupExpiredSessions(ctx context.Context) error {
	return h.userRepo.CleanupExpiredSessions(ctx)
}

// currentSessionRef identifies which refresh session/family the caller is
// authenticated as. Preferred source: sid/fam claims embedded in interactive
// access tokens at mint time. Fallback for legacy access tokens without
// lineage claims: resolve from the refresh cookie or body token.
func (h *AuthHandler) currentSessionRef(c *gin.Context) (sessionID, familyID string) {
	claims := jwt.ExtractClaims(c)
	sessionID, familyID = SessionRefFromClaims(map[string]interface{}(claims))
	if familyID != "" {
		return sessionID, familyID
	}
	raw, _ := c.Cookie(RefreshTokenCookieName)
	if raw == "" {
		var req RefreshRequest
		if err := c.ShouldBindJSON(&req); err == nil {
			raw = req.RefreshToken
		}
	}
	return h.tokenService.SessionRefForToken(c.Request.Context(), raw)
}

// ListSessionsHandler handles GET /auth/sessions: the caller's own live
// sessions with safe metadata only.
func (h *AuthHandler) ListSessionsHandler(c *gin.Context) {
	currentUser, ok := CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	currentSessionID, _ := h.currentSessionRef(c)

	sessions, err := h.tokenService.ListUserSessions(c.Request.Context(), currentUser.ID, currentSessionID)
	if err != nil {
		logging.FromContext(c).Errorw("Failed to list sessions", "error", err, "userID", currentUser.ID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to list sessions"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": gin.H{"sessions": sessions}})
}

// RevokeSessionHandler handles DELETE /auth/sessions/:id. The :id is a
// rotation-family ID; ownership is enforced server-side so users can only
// revoke their own sessions.
func (h *AuthHandler) RevokeSessionHandler(c *gin.Context) {
	currentUser, ok := CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	familyID := c.Param("id")
	if familyID == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Session id required"})
		return
	}

	found, err := h.tokenService.RevokeUserSessionFamily(c.Request.Context(), currentUser.ID, familyID)
	if err != nil {
		logging.FromContext(c).Errorw("Failed to revoke session", "error", err, "userID", currentUser.ID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to revoke session"})
		return
	}
	if !found {
		c.JSON(http.StatusNotFound, gin.H{"error": "Session not found"})
		return
	}

	// Revoking your own current session is a logout: drop the refresh cookie.
	_, currentFamily := h.currentSessionRef(c)
	if currentFamily == familyID {
		ClearRefreshSessionCookie(c)
	}
	c.JSON(http.StatusOK, gin.H{"message": "Session revoked"})
}

// RevokeOtherSessionsHandler handles POST /auth/sessions/revoke-others:
// revokes every family except the caller's current one.
func (h *AuthHandler) RevokeOtherSessionsHandler(c *gin.Context) {
	currentUser, ok := CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	_, currentFamily := h.currentSessionRef(c)

	if err := h.tokenService.RevokeOtherUserFamilies(c.Request.Context(), currentUser.ID, currentFamily); err != nil {
		logging.FromContext(c).Errorw("Failed to revoke other sessions", "error", err, "userID", currentUser.ID)
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to revoke other sessions"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"message": "All other sessions revoked"})
}
