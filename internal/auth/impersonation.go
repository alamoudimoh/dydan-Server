package auth

import (
	"strconv"

	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/dydan-server/internal/circle/repo"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
	"github.com/alamoudimoh/dydan-server/logging"
	"github.com/gin-gonic/gin"
)

const (
	ImpersonateUserIDHeader = "X-Impersonate-User-ID"
	ImpersonatedUserKey     = "impersonated_user"
	ActualUserKey           = "id"
)

// ImpersonationMiddleware handles user impersonation for admin/manager users
func ImpersonationMiddleware(userRepo *uRepo.UserRepository, circleRepo *cRepo.CircleRepository) gin.HandlerFunc {
	return func(c *gin.Context) {
		logger := logging.FromContext(c)

		// Get the current authenticated user
		currentUser, ok := CurrentUser(c)
		if !ok {
			c.Next()
			return
		}

		// Check if impersonation is requested
		impersonateUserIDStr := c.GetHeader(ImpersonateUserIDHeader)
		if impersonateUserIDStr == "" {
			c.Next()
			return
		}

		// Parse the impersonate user ID
		impersonateUserID, err := strconv.Atoi(impersonateUserIDStr)
		if err != nil {
			logger.Error("Invalid impersonate user ID", "impersonateUserID", impersonateUserIDStr, "error", err)
			c.JSON(400, gin.H{"error": "Invalid impersonate user ID"})
			c.Abort()
			return
		}

		// Get circle users to validate permissions and membership
		circleUsers, err := circleRepo.GetCircleUsers(c, currentUser.CircleID)
		if err != nil {
			logger.Error("Failed to get circle users", "error", err, "circleID", currentUser.CircleID)
			c.JSON(500, gin.H{"error": "Failed to validate permissions"})
			c.Abort()
			return
		}

		var currentUserRole cModel.UserRole
		var currentUserActive bool
		var impersonateUserInCircle bool
		var impersonateUserActive bool

		for _, circleUser := range circleUsers {
			if circleUser.UserID == currentUser.ID {
				currentUserRole = circleUser.Role
				currentUserActive = circleUser.IsActive
			}
			if circleUser.UserID == impersonateUserID {
				impersonateUserInCircle = true
				impersonateUserActive = circleUser.IsActive
			}
		}

		// Managed (child) accounts never gain elevated authority — including
		// impersonation — regardless of stale membership rows.
		if currentUser.UserType == uModel.UserTypeChild {
			logger.Warn("Child account attempted impersonation",
				"userID", currentUser.ID, "impersonateUserID", impersonateUserID)
			c.JSON(403, gin.H{"error": "Insufficient permissions for impersonation"})
			c.Abort()
			return
		}

		// Validate current user has an active admin or manager role. Stale
		// (inactive) memberships must not preserve impersonation rights.
		if !currentUserActive || (currentUserRole != cModel.UserRoleAdmin && currentUserRole != cModel.UserRoleManager) {
			logger.Warn("User attempted impersonation without permission",
				"userID", currentUser.ID, "role", currentUserRole, "active", currentUserActive, "impersonateUserID", impersonateUserID)
			c.JSON(403, gin.H{"error": "Insufficient permissions for impersonation"})
			c.Abort()
			return
		}

		// Validate impersonate user is an active member of the same circle.
		if !impersonateUserInCircle || !impersonateUserActive {
			logger.Warn("User attempted to impersonate user outside their active circle",
				"userID", currentUser.ID, "impersonateUserID", impersonateUserID, "circleID", currentUser.CircleID)
			c.JSON(403, gin.H{"error": "Cannot impersonate user outside your circle"})
			c.Abort()
			return
		}

		// Get full user details for the impersonated user
		impersonatedUser, err := userRepo.GetUserByID(c, impersonateUserID)
		if err != nil {
			logger.Error("Failed to get impersonated user details", "error", err, "impersonateUserID", impersonateUserID)
			c.JSON(500, gin.H{"error": "Failed to get impersonated user details"})
			c.Abort()
			return
		}

		// Create UserDetails for the impersonated user by copying all fields
		impersonatedUserDetails := &uModel.UserDetails{
			User:       *impersonatedUser,
			WebhookURL: nil, // This will be populated from circle if needed
		}

		// Clear the password for security
		impersonatedUserDetails.Password = ""

		// Store both users in context
		c.Set(ActualUserKey, currentUser)
		c.Set(ImpersonatedUserKey, impersonatedUserDetails)

		logger.Debugw("User impersonation started",
			"actualUserID", currentUser.ID,
			"actualUsername", currentUser.Username,
			"impersonatedUserID", impersonatedUser.ID,
			"impersonatedUsername", impersonatedUser.Username)

		c.Next()
	}
}

// CurrentUserWithImpersonation returns both the actual user and impersonated user (if any)
// Returns (actualUser, impersonatedUser, hasImpersonation)
func CurrentUserWithImpersonation(c *gin.Context) (*uModel.UserDetails, *uModel.UserDetails, bool) {
	actualUser, ok := c.Get(ActualUserKey)
	if !ok {
		// No impersonation, return current user
		currentUser, exists := CurrentUser(c)
		return currentUser, nil, exists && false
	}

	impersonatedUser, impersonationExists := c.Get(ImpersonatedUserKey)
	if !impersonationExists {
		// Fallback to current user
		currentUser, exists := CurrentUser(c)
		return currentUser, nil, exists && false
	}

	actualUserDetails, actualOk := actualUser.(*uModel.UserDetails)
	impersonatedUserDetails, impersonatedOk := impersonatedUser.(*uModel.UserDetails)

	if !actualOk || !impersonatedOk {
		// Fallback to current user
		currentUser, exists := CurrentUser(c)
		return currentUser, nil, exists && false
	}

	return actualUserDetails, impersonatedUserDetails, true
}

// GetEffectiveUser returns the user that should be used for operations
// If impersonating, returns the impersonated user, otherwise returns the current user
func GetEffectiveUser(c *gin.Context) (*uModel.UserDetails, bool) {
	_, impersonatedUser, hasImpersonation := CurrentUserWithImpersonation(c)
	if hasImpersonation {
		return impersonatedUser, true
	}

	return CurrentUser(c)
}

// GetActualUser returns the actual authenticated user (ignoring impersonation)
func GetActualUser(c *gin.Context) (*uModel.UserDetails, bool) {
	actualUser, _, hasImpersonation := CurrentUserWithImpersonation(c)
	if hasImpersonation {
		return actualUser, true
	}

	return CurrentUser(c)
}
