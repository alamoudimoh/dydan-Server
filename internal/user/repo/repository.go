package user

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	nModel "github.com/alamoudimoh/dydan-server/internal/notifier/model"
	storageModel "github.com/alamoudimoh/dydan-server/internal/storage/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/alamoudimoh/dydan-server/logging"
	modernsqlite "github.com/glebarez/go-sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrInvalidPasswordResetToken = errors.New("invalid password reset token")

// ErrUserAlreadyExists reports that the submitted signup identity (username or
// email) collides with an existing account. It is an expected, client-facing
// condition — not an internal failure.
var ErrUserAlreadyExists = errors.New("an account with this username or email already exists")

const passwordResetLifetime = time.Hour

func passwordResetTokenHash(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

type IUserRepository interface {
	GetUserByUsername(username string) (*uModel.UserDetails, error)
	GetUser(id int) (*uModel.User, error)
	GetAllUsers() ([]*uModel.User, error)
	CreateUser(user *uModel.User) error
	UpdateUser(user *uModel.User) error
	UpdateUserCircle(userID, circleID int) error
	FindByEmail(email string) (*uModel.User, error)
	// MFA-related methods
	EnableMFA(c context.Context, userID int, secret string, backupCodes []string) error
	DisableMFA(c context.Context, userID int) error
	UpdateMFARecoveryCodes(c context.Context, userID int, usedCodes string) error
	// MFA Session methods
	CreateMFASession(c context.Context, session *uModel.MFASession) error
	GetMFASession(c context.Context, sessionToken string) (*uModel.MFASession, error)
	UpdateMFASession(c context.Context, session *uModel.MFASession) error
	DeleteMFASession(c context.Context, sessionToken string) error
	CleanupExpiredMFASessions(c context.Context) error
	// Device Token methods
	RegisterDeviceToken(c context.Context, deviceToken *uModel.UserDeviceToken) error
	UnregisterDeviceToken(c context.Context, userID int, deviceID string) error
	GetUserDeviceTokens(c context.Context, userID int) ([]*uModel.UserDeviceToken, error)
	GetActiveDeviceTokens(c context.Context, userID int) ([]*uModel.UserDeviceToken, error)
	UpdateDeviceTokenActivity(c context.Context, userID int, deviceID string) error
	// UserSession methods for refresh token management
	CreateUserSession(c context.Context, session *uModel.UserSession, credentialVersion int) error
	GetUserSessionByTokenHash(c context.Context, tokenHash string) (*uModel.UserSession, error)
	GetLiveUserSessions(c context.Context, userID int) ([]*uModel.UserSession, error)
	MarkSessionUsed(c context.Context, sessionID string) error
	MarkSessionUsedIfUnused(c context.Context, sessionID string) (bool, error)
	GetLatestFamilySession(c context.Context, familyID string) (*uModel.UserSession, error)
	RevokeSession(c context.Context, sessionID string) error
	RevokeSessionFamily(c context.Context, familyID string) error
	RevokeAllUserSessions(c context.Context, userID int) error
	RevokeOtherUserSessions(c context.Context, userID int, keepSessionID string) error
	CleanupExpiredSessions(c context.Context) error
	GetUserPreferences(c context.Context, userID int) (*uModel.UserPreference, error)
	SaveUserPreferences(c context.Context, preference *uModel.UserPreference) error
}

type UserRepository struct {
	db           *gorm.DB
	isDydanCloud bool
}

func (r *UserRepository) GetUserPreferences(c context.Context, userID int) (*uModel.UserPreference, error) {
	var preference uModel.UserPreference
	err := r.db.WithContext(c).Where("user_id = ?", userID).First(&preference).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &preference, nil
}

func (r *UserRepository) SaveUserPreferences(c context.Context, preference *uModel.UserPreference) error {
	return r.db.WithContext(c).Save(preference).Error
}

func NewUserRepository(db *gorm.DB, cfg *config.Config) *UserRepository {
	return &UserRepository{db, cfg.IsDydanCloud}
}

func (r *UserRepository) GetAllUsers(c context.Context, circleID int) ([]*uModel.User, error) {
	var users []*uModel.User
	if err := r.db.WithContext(c).Where("circle_id = ?", circleID).Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}

func (r *UserRepository) GetAllUsersForSystemOnly(c context.Context) ([]*uModel.User, error) {
	var users []*uModel.User
	if err := r.db.WithContext(c).Find(&users).Error; err != nil {
		return nil, err
	}
	return users, nil
}
func (r *UserRepository) CreateUser(c context.Context, user *uModel.User) (*uModel.User, error) {
	user.Email = strings.ToLower(strings.TrimSpace(user.Email))
	// Expected duplicate identities are classified up front (mirroring the
	// label-repo sentinel-error convention) so the handler can answer 409
	// instead of surfacing an internal server error. The unique indexes remain
	// the authoritative guard against concurrent-signup races.
	var count int64
	if err := r.db.WithContext(c).Model(&uModel.User{}).
		Where("username = ? OR (email != '' AND email = ?)", user.Username, user.Email).
		Count(&count).Error; err != nil {
		return nil, err
	}
	if count > 0 {
		return nil, ErrUserAlreadyExists
	}
	if err := r.db.WithContext(c).Create(user).Error; err != nil {
		// The pre-check above cannot close the race between two concurrent
		// signups of the same identity; the unique indexes are the
		// authoritative guard. Classify that constraint violation as the same
		// expected duplicate condition so both paths yield 409 upstream.
		if classifyDuplicateIdentity(err) {
			return nil, ErrUserAlreadyExists
		}
		return nil, err
	}
	return user, nil
}

// CreateChildWithMembership persists a managed account and its Circle
// membership as one unit. A child without that membership cannot be used and
// must never be left behind after a partial failure.
func (r *UserRepository) CreateChildWithMembership(c context.Context, user *uModel.User, membership *cModel.UserCircle) (*uModel.User, error) {
	user.Email = strings.ToLower(strings.TrimSpace(user.Email))
	err := r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(user).Error; err != nil {
			return err
		}
		membership.UserID = user.ID
		return tx.Create(membership).Error
	})
	if err != nil {
		return nil, err
	}
	return user, nil
}
func (r *UserRepository) GetUserByUsername(c context.Context, username string) (*uModel.UserDetails, error) {
	var user *uModel.UserDetails
	if r.isDydanCloud {
		now := time.Now().UTC()
		if err := r.db.WithContext(c).Preload("UserNotificationTargets").Table("users u").Select("u.*, s.status as subscription, s.expires_at as expiration, c.webhook_url as webhook_url").Joins("left join subscriptions s on s.circle_id = u.circle_id AND s.status = 'active' AND (s.expires_at IS NULL OR s.expires_at > ?)", now).Joins("left join circles c on c.id = u.circle_id").Where("username = ?", username).First(&user).Error; err != nil {
			return nil, err
		}
	} else {
		// For self-hosted, first get the user without subscription/expiration fields
		if err := r.db.WithContext(c).Preload("UserNotificationTargets").Table("users u").Select("u.*, c.webhook_url as webhook_url").Joins("left join circles c on c.id = u.circle_id").Where("username = ?", username).First(&user).Error; err != nil {
			return nil, err
		}
		// Then manually set the subscription status and expiration for self-hosted users
		subscription := "active"
		futureDate := time.Date(2999, 12, 31, 0, 0, 0, 0, time.UTC)
		user.Subscription = &subscription
		user.Expiration = &futureDate
	}

	return user, nil
}

func (r *UserRepository) GetUserByID(c context.Context, userID int) (*uModel.User, error) {
	var user *uModel.User
	now := time.Now().UTC()
	if r.isDydanCloud {
		if err := r.db.WithContext(c).Preload("UserNotificationTargets").
			Table("users u").
			Select("u.*, s.status as subscription, s.expires_at as expiration, c.webhook_url as webhook_url").
			Joins("left join subscriptions s on s.circle_id = u.circle_id AND s.status = 'active' AND s.expires_at > ?", now).
			Joins("left join circles c on c.id = u.circle_id").
			Where("u.id = ?", userID).
			First(&user).Error; err != nil {
			return nil, err
		}
	} else {
		if err := r.db.WithContext(c).Preload("UserNotificationTargets").
			Table("users u").
			Select("u.*, c.webhook_url as webhook_url").
			Joins("left join circles c on c.id = u.circle_id").
			Where("u.id = ?", userID).
			First(&user).Error; err != nil {
			return nil, err
		}
		// Set default subscription/expiration for self-hosted
		subscription := "active"
		futureDate := time.Date(2999, 12, 31, 0, 0, 0, 0, time.UTC)

		user.Subscription = &subscription
		user.Expiration = &futureDate

	}
	return user, nil
}

func (r *UserRepository) GetChildUsersByParentID(c context.Context, parentID int) ([]*uModel.User, error) {
	var childUsers []*uModel.User
	if err := r.db.WithContext(c).Where("parent_user_id = ? AND user_type = ?", parentID, uModel.UserTypeChild).Find(&childUsers).Error; err != nil {
		return nil, err
	}
	return childUsers, nil
}

func (r *UserRepository) GetChildUserCount(c context.Context, parentID int) (int64, error) {
	var count int64
	if err := r.db.WithContext(c).Model(&uModel.User{}).Where("parent_user_id = ? AND user_type = ?", parentID, uModel.UserTypeChild).Count(&count).Error; err != nil {
		return 0, err
	}
	return count, nil
}

func (r *UserRepository) UpdateUser(c context.Context, user *uModel.User) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if err := tx.Save(user).Error; err != nil {
			return err
		}
		if user.CircleID != 0 {
			if err := tx.Model(&storageModel.StorageUsage{}).
				Where("user_id = ? AND circle_id = 0", user.ID).
				Update("circle_id", user.CircleID).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (r *UserRepository) UpdateUserCircle(c context.Context, userID, circleID int) error {
	return r.db.WithContext(c).Model(&uModel.User{}).Where("id = ?", userID).Update("circle_id", circleID).Error
}

func (r *UserRepository) FindByEmail(c context.Context, email string) (*uModel.UserDetails, error) {
	var users []uModel.UserDetails
	normalized := strings.ToLower(strings.TrimSpace(email))
	if err := r.db.WithContext(c).Table("users u").Select("u.*, c.webhook_url as webhook_url").Joins("left join circles c on c.id = u.circle_id").Where("LOWER(email) = ?", normalized).Limit(2).Find(&users).Error; err != nil {
		return nil, err
	}
	if len(users) == 0 {
		return nil, gorm.ErrRecordNotFound
	}
	if len(users) > 1 {
		return nil, fmt.Errorf("multiple accounts use the same normalized email")
	}
	return &users[0], nil
}

func (r *UserRepository) SetPasswordResetToken(c context.Context, email, token string) error {
	// confirm user exists with email:
	user, err := r.FindByEmail(c, email)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", user.ID).Delete(&uModel.UserPasswordReset{}).Error; err != nil {
			return err
		}
		return tx.Create(&uModel.UserPasswordReset{
			UserID:         user.ID,
			Token:          passwordResetTokenHash(token),
			Email:          strings.ToLower(strings.TrimSpace(user.Email)),
			CreatedAt:      now,
			ExpirationDate: now.Add(passwordResetLifetime),
		}).Error
	})
}

func (r *UserRepository) DeletePasswordResetToken(c context.Context, token string) error {
	return r.db.WithContext(c).Where("token IN ?", []string{token, passwordResetTokenHash(token)}).Delete(&uModel.UserPasswordReset{}).Error
}

func (r *UserRepository) CleanupExpiredPasswordResets(c context.Context) error {
	return r.db.WithContext(c).Where("expiration_date <= ?", time.Now().UTC()).Delete(&uModel.UserPasswordReset{}).Error
}

func (r *UserRepository) UpdatePasswordByToken(ctx context.Context, email string, token string, password string) error {
	logger := logging.FromContext(ctx)

	logger.Debug("account.db.UpdatePasswordByToken")
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reset uModel.UserPasswordReset
		normalizedEmail := strings.ToLower(strings.TrimSpace(email))
		acceptedTokens := []string{token, passwordResetTokenHash(token)}
		if err := tx.Where("LOWER(email) = ? AND token IN ? AND expiration_date > ?", normalizedEmail, acceptedTokens, time.Now().UTC()).First(&reset).Error; err != nil {
			return ErrInvalidPasswordResetToken
		}
		// Conditional delete is the serialization point: concurrent redemption
		// attempts may both read the row, but only one can consume it.
		deleted := tx.Where("token = ?", reset.Token).Delete(&uModel.UserPasswordReset{})
		if deleted.Error != nil {
			return deleted.Error
		}
		if deleted.RowsAffected != 1 {
			return ErrInvalidPasswordResetToken
		}

		chain := tx.Model(&uModel.User{}).Where("id = ? AND LOWER(email) = ?", reset.UserID, normalizedEmail).Updates(map[string]interface{}{
			"password":           password,
			"credential_version": gorm.Expr("credential_version + 1"),
		})
		if chain.Error != nil {
			return chain.Error
		}
		if chain.RowsAffected == 0 {
			return fmt.Errorf("account not found")
		}

		now := time.Now().UTC()
		if err := tx.Model(&uModel.UserSession{}).
			Where("user_id = ? AND revoked_at IS NULL", reset.UserID).
			Update("revoked_at", now).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", reset.UserID).Delete(&uModel.MFASession{}).Error; err != nil {
			return err
		}
		return nil
	})
}

func (r *UserRepository) StoreAPIToken(c context.Context, userID int, name string, tokenCode string) (*uModel.APIToken, error) {
	token := &uModel.APIToken{
		UserID: userID,
		Name:   name,
		// Only the digest is persisted; the plaintext secret is returned to
		// the caller exactly once in the creation response and never stored.
		Token:     APITokenSecretHash(tokenCode),
		CreatedAt: time.Now().UTC(),
	}
	if err := r.db.WithContext(c).Model(&uModel.APIToken{}).Save(
		token).Error; err != nil {
		return nil, err

	}
	return token, nil
}

// GetUserByToken authenticates a presented API-token secret and resolves its
// owner. Lookup complexity is unchanged from the plaintext design: one
// indexed equality probe on api_tokens.token, now against the SHA-256 digest
// of the presented secret (plus a legacy candidate so pre-migration plaintext
// rows keep working), followed by the existing single user-details join keyed
// on the resolved token row id.
func (r *UserRepository) GetUserByToken(c context.Context, token string) (*uModel.UserDetails, error) {
	if !isWellFormedAPITokenSecret(token) {
		return nil, gorm.ErrRecordNotFound
	}

	// Resolve the credential row. At most one of the candidates can exist:
	// hashed rows store "sha256:<digest>", legacy rows store the raw secret.
	var cred struct {
		ID    int
		Token string
	}
	err := r.db.WithContext(c).
		Table("api_tokens").
		Select("id, token").
		Where("token IN ?", []string{APITokenSecretHash(token), token}).
		First(&cred).Error
	if err != nil {
		return nil, err
	}

	// Transitional path: a legacy plaintext row matched. Verify it in
	// constant time, upgrade it to hashed storage for good, and continue.
	if isStoredFormatToken(cred.Token) {
		if !constantTimeTokenEqual(APITokenSecretHash(token), cred.Token) {
			return nil, gorm.ErrRecordNotFound
		}
	} else {
		if !constantTimeTokenEqual(token, cred.Token) {
			return nil, gorm.ErrRecordNotFound
		}
		r.upgradeLegacyAPIToken(c, cred.Token)
	}

	var user *uModel.UserDetails
	now := time.Now().UTC()

	if r.isDydanCloud {
		if err := r.db.WithContext(c).Table("users u").Select("u.*, s.status as subscription, s.expires_at as expiration, c.webhook_url as webhook_url").Joins("left join api_tokens at on at.user_id = u.id").Joins("left join subscriptions s on s.circle_id = u.circle_id AND s.status = 'active' AND (s.expires_at IS NULL OR s.expires_at > ?)", now).Joins("left join circles c on c.id = u.circle_id").Where("at.id = ?", cred.ID).First(&user).Error; err != nil {
			return nil, err
		}
	} else {
		if err := r.db.WithContext(c).Table("users u").Select("u.*, c.webhook_url as webhook_url").Joins("left join api_tokens at on at.user_id = u.id").Joins("left join circles c on c.id = u.circle_id").Where("at.id = ?", cred.ID).First(&user).Error; err != nil {
			return nil, err
		}
		// Set default subscription/expiration for self-hosted
		subscription := "active"
		futureDate := time.Date(2999, 12, 31, 0, 0, 0, 0, time.UTC)
		user.Subscription = &subscription
		user.Expiration = &futureDate
	}

	return user, nil
}

func (r *UserRepository) GetAllUserTokens(c context.Context, userID int) ([]*uModel.APIToken, error) {
	var tokens []*uModel.APIToken
	if err := r.db.WithContext(c).Where("user_id = ?", userID).Find(&tokens).Error; err != nil {
		return nil, err
	}
	return tokens, nil
}

func (r *UserRepository) DeleteAPIToken(c context.Context, userID int, tokenID string) error {
	return r.db.WithContext(c).Where("id = ? AND user_id = ?", tokenID, userID).Delete(&uModel.APIToken{}).Error
}

func (r *UserRepository) UpdateNotificationTarget(c context.Context, userID int, targetID string, targetType nModel.NotificationPlatform) error {
	return r.db.WithContext(c).Save(&uModel.UserNotificationTarget{
		UserID:    userID,
		TargetID:  targetID,
		Type:      targetType,
		CreatedAt: time.Now().UTC(),
	}).Error
}

func (r *UserRepository) DeleteNotificationTarget(c context.Context, userID int) error {
	return r.db.WithContext(c).Where("user_id = ?", userID).Delete(&uModel.UserNotificationTarget{}).Error
}

func (r *UserRepository) UpdateNotificationTargetForAllNotifications(c context.Context, userID int, targetID string, targetType nModel.NotificationPlatform) error {
	return r.db.WithContext(c).Model(&nModel.Notification{}).Where("user_id = ?", userID).Update("target_id", targetID).Update("type", targetType).Error
}
func (r *UserRepository) ChangeChildPasswordAndRevokeSessions(c context.Context, userID int, password string) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&uModel.User{}).Where("id = ?", userID).Updates(map[string]interface{}{
			"password":           password,
			"credential_version": gorm.Expr("credential_version + 1"),
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return tx.Model(&uModel.UserSession{}).
			Where("user_id = ? AND revoked_at IS NULL", userID).
			Update("revoked_at", time.Now().UTC()).Error
	})
}

func (r *UserRepository) ChangePasswordAndRevokeOtherFamilies(c context.Context, userID int, password, keepFamilyID string) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		updated := tx.Model(&uModel.User{}).Where("id = ?", userID).Updates(map[string]interface{}{
			"password":           password,
			"credential_version": gorm.Expr("credential_version + 1"),
		})
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		query := tx.Model(&uModel.UserSession{}).Where("user_id = ? AND revoked_at IS NULL", userID)
		if keepFamilyID != "" {
			query = query.Where("family_id <> ?", keepFamilyID)
		}
		return query.Update("revoked_at", time.Now().UTC()).Error
	})
}
func (r *UserRepository) UpdateUserImage(c context.Context, userID int, image string) error {
	return r.db.WithContext(c).Model(&uModel.User{}).Where("id = ?", userID).Update("image", image).Error
}

// MFA-related methods

// EnableMFA enables MFA for a user with the provided secret and backup codes
func (r *UserRepository) EnableMFA(c context.Context, userID int, secret string, backupCodes []string) error {
	backupCodesJSON, err := json.Marshal(backupCodes)
	if err != nil {
		return err
	}

	return r.changeMFAStateAndRevokeSessions(c, userID, map[string]interface{}{
		"mfa_enabled":             true,
		"mfa_secret":              secret,
		"mfa_backup_codes":        string(backupCodesJSON),
		"mfa_recovery_codes_used": "[]",
	})
}

// DisableMFA disables MFA for a user
func (r *UserRepository) DisableMFA(c context.Context, userID int) error {
	return r.changeMFAStateAndRevokeSessions(c, userID, map[string]interface{}{
		"mfa_enabled":             false,
		"mfa_secret":              "",
		"mfa_backup_codes":        "",
		"mfa_recovery_codes_used": "",
	})
}

func (r *UserRepository) changeMFAStateAndRevokeSessions(c context.Context, userID int, updates map[string]interface{}) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		updates["credential_version"] = gorm.Expr("credential_version + 1")
		updated := tx.Model(&uModel.User{}).Where("id = ?", userID).Updates(updates)
		if updated.Error != nil {
			return updated.Error
		}
		if updated.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		return tx.Model(&uModel.UserSession{}).
			Where("user_id = ? AND revoked_at IS NULL", userID).
			Update("revoked_at", time.Now().UTC()).Error
	})
}

// UpdateMFARecoveryCodes updates the used recovery codes for a user
func (r *UserRepository) UpdateMFARecoveryCodes(c context.Context, userID int, usedCodes string) error {
	return r.db.WithContext(c).Model(&uModel.User{}).Where("id = ?", userID).Update("mfa_recovery_codes_used", usedCodes).Error
}

// MFA Session methods

// CreateMFASession creates a new MFA session
func (r *UserRepository) CreateMFASession(c context.Context, session *uModel.MFASession) error {
	return r.db.WithContext(c).Create(session).Error
}

// GetMFASession retrieves an MFA session by token
func (r *UserRepository) GetMFASession(c context.Context, sessionToken string) (*uModel.MFASession, error) {
	var session uModel.MFASession
	if err := r.db.WithContext(c).Where("session_token = ? AND expires_at > ?", sessionToken, time.Now().UTC()).First(&session).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

// UpdateMFASession updates an MFA session
func (r *UserRepository) UpdateMFASession(c context.Context, session *uModel.MFASession) error {
	return r.db.WithContext(c).Save(session).Error
}

// DeleteMFASession deletes an MFA session
func (r *UserRepository) DeleteMFASession(c context.Context, sessionToken string) error {
	return r.db.WithContext(c).Where("session_token = ?", sessionToken).Delete(&uModel.MFASession{}).Error
}

func (r *UserRepository) ConsumeMFASession(c context.Context, sessionToken string, userID int, expectedUsedCodes, newUsedCodes string) (bool, error) {
	consumed := false
	err := r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		res := tx.Where(
			"session_token = ? AND user_id = ? AND verified = ? AND expires_at > ?",
			sessionToken, userID, false, time.Now().UTC(),
		).Delete(&uModel.MFASession{})
		if res.Error != nil || res.RowsAffected != 1 {
			return res.Error
		}
		if newUsedCodes != expectedUsedCodes {
			res = tx.Model(&uModel.User{}).
				Where("id = ? AND mfa_recovery_codes_used = ?", userID, expectedUsedCodes).
				Update("mfa_recovery_codes_used", newUsedCodes)
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return gorm.ErrRecordNotFound
			}
		}
		consumed = true
		return nil
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return consumed, err
}

func (r *UserRepository) ReserveMFAAttempt(c context.Context, sessionToken string, maxAttempts int) (int, bool, error) {
	res := r.db.WithContext(c).Model(&uModel.MFASession{}).
		Where("session_token = ? AND verified = ? AND expires_at > ? AND attempts < ?", sessionToken, false, time.Now().UTC(), maxAttempts).
		UpdateColumn("attempts", gorm.Expr("attempts + 1"))
	if res.Error != nil || res.RowsAffected != 1 {
		return 0, false, res.Error
	}
	var session uModel.MFASession
	if err := r.db.WithContext(c).Select("attempts").Where("session_token = ?", sessionToken).First(&session).Error; err != nil {
		return 0, false, err
	}
	return session.Attempts, true, nil
}

// CleanupExpiredMFASessions removes expired MFA sessions
func (r *UserRepository) CleanupExpiredMFASessions(c context.Context) error {
	return r.db.WithContext(c).Where("expires_at < ?", time.Now().UTC()).Delete(&uModel.MFASession{}).Error
}

// UserSession methods for refresh token management

// CreateUserSession creates a new user session (refresh token)
func lockUserSessionWrites(tx *gorm.DB, userID int) (*uModel.User, error) {
	var user uModel.User
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Select("id", "credential_version").First(&user, userID).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) CreateUserSession(c context.Context, session *uModel.UserSession, credentialVersion int) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		user, err := lockUserSessionWrites(tx, session.UserID)
		if err != nil {
			return err
		}
		if user.CredentialVersion != credentialVersion {
			return fmt.Errorf("credentials changed during authentication")
		}
		return tx.Create(session).Error
	})
}

// GetUserSessionByTokenHash retrieves a user session by token hash
func (r *UserRepository) GetUserSessionByTokenHash(c context.Context, tokenHash string) (*uModel.UserSession, error) {
	var session uModel.UserSession
	if err := r.db.WithContext(c).Where("token_hash = ? AND expires_at > ? AND revoked_at IS NULL", tokenHash, time.Now().UTC()).First(&session).Error; err != nil {
		return nil, err
	}
	return &session, nil
}

// MarkSessionUsed marks a session as used (for token rotation detection)
func (r *UserRepository) MarkSessionUsed(c context.Context, sessionID string) error {
	now := time.Now().UTC()
	return r.db.WithContext(c).Model(&uModel.UserSession{}).Where("id = ?", sessionID).Update("used_at", now).Error
}

// MarkSessionUsedIfUnused atomically marks a session used only if no other
// request has consumed it yet. It returns false when the row was already
// used, which is the serialization point for concurrent refreshes.
func (r *UserRepository) MarkSessionUsedIfUnused(c context.Context, sessionID string) (bool, error) {
	now := time.Now().UTC()
	res := r.db.WithContext(c).Model(&uModel.UserSession{}).
		Where("id = ? AND used_at IS NULL", sessionID).
		Update("used_at", now)
	return res.RowsAffected > 0, res.Error
}

const refreshRotationSQLiteRetries = 5

const (
	sqliteBusyCode   = 5
	sqliteLockedCode = 6
)

func isRetryableSQLiteLock(err error) bool {
	var sqliteErr *modernsqlite.Error
	if !errors.As(err, &sqliteErr) {
		return false
	}
	code := sqliteErr.Code() & 0xff
	return code == sqliteBusyCode || code == sqliteLockedCode
}

func (r *UserRepository) RotateUserSession(c context.Context, oldSessionID string, next *uModel.UserSession) (bool, error) {
	for attempt := 0; attempt <= refreshRotationSQLiteRetries; attempt++ {
		won := false
		err := r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
			if _, err := lockUserSessionWrites(tx, next.UserID); err != nil {
				return err
			}
			res := tx.Model(&uModel.UserSession{}).
				Where("id = ? AND used_at IS NULL AND revoked_at IS NULL AND expires_at > ?", oldSessionID, time.Now().UTC()).
				Update("used_at", time.Now().UTC())
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return nil
			}
			if err := tx.Create(next).Error; err != nil {
				return err
			}
			won = true
			return nil
		})
		if err == nil || !isRetryableSQLiteLock(err) || attempt == refreshRotationSQLiteRetries {
			return won, err
		}
		select {
		case <-c.Done():
			return false, c.Err()
		case <-time.After(time.Duration(attempt+1) * time.Millisecond):
		}
	}
	return false, nil
}

// GetLatestFamilySession returns the newest live (non-revoked, unexpired)
// session in a rotation family, i.e. the current head of the chain.
func (r *UserRepository) GetLatestFamilySession(c context.Context, familyID string) (*uModel.UserSession, error) {
	var session uModel.UserSession
	err := r.db.WithContext(c).
		Where("family_id = ? AND revoked_at IS NULL AND expires_at > ?", familyID, time.Now().UTC()).
		Order("created_at DESC").First(&session).Error
	if err != nil {
		return nil, err
	}
	return &session, nil
}

func (r *UserRepository) IsUserSessionFamilyLive(c context.Context, userID int, familyID string) (bool, error) {
	var count int64
	err := r.db.WithContext(c).Model(&uModel.UserSession{}).
		Where("user_id = ? AND family_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, familyID, time.Now().UTC()).
		Limit(1).Count(&count).Error
	return count > 0, err
}

// IsUserSessionLive reports whether the exact session row is still live.
// Because revoking a family stamps every row in it, a live row implies its
// family is live, so this single check subsumes family liveness for tokens
// that carry a session reference.
func (r *UserRepository) IsUserSessionLive(c context.Context, userID int, sessionID string) (bool, error) {
	var count int64
	err := r.db.WithContext(c).Model(&uModel.UserSession{}).
		Where("user_id = ? AND id = ? AND revoked_at IS NULL AND expires_at > ?", userID, sessionID, time.Now().UTC()).
		Limit(1).Count(&count).Error
	return count > 0, err
}

// RevokeSession revokes a specific session
func (r *UserRepository) RevokeSession(c context.Context, sessionID string) error {
	now := time.Now().UTC()
	return r.db.WithContext(c).Model(&uModel.UserSession{}).Where("id = ?", sessionID).Update("revoked_at", now).Error
}

// RevokeSessionFamily revokes all sessions in a token family (security measure)
func (r *UserRepository) RevokeSessionFamily(c context.Context, familyID string) error {
	now := time.Now().UTC()
	return r.db.WithContext(c).Model(&uModel.UserSession{}).Where("family_id = ? AND revoked_at IS NULL", familyID).Update("revoked_at", now).Error
}

func (r *UserRepository) RevokeUserSessionFamily(c context.Context, userID int, familyID string) (bool, error) {
	found := false
	err := r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if _, err := lockUserSessionWrites(tx, userID); err != nil {
			return err
		}
		res := tx.Model(&uModel.UserSession{}).
			Where("user_id = ? AND family_id = ? AND revoked_at IS NULL", userID, familyID).
			Update("revoked_at", time.Now().UTC())
		found = res.RowsAffected > 0
		return res.Error
	})
	return found, err
}

// RevokeUserSessionByID revokes exactly one session row after verifying it
// belongs to userID. Other sessions in the same rotation family stay live,
// so their access JWTs keep working while the revoked session's tokens die.
func (r *UserRepository) RevokeUserSessionByID(c context.Context, userID int, sessionID string) (bool, error) {
	found := false
	err := r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if _, err := lockUserSessionWrites(tx, userID); err != nil {
			return err
		}
		res := tx.Model(&uModel.UserSession{}).
			Where("user_id = ? AND id = ? AND revoked_at IS NULL", userID, sessionID).
			Update("revoked_at", time.Now().UTC())
		found = res.RowsAffected > 0
		return res.Error
	})
	return found, err
}

// GetLiveUserSessions returns every non-revoked, unexpired refresh session
// belonging to a user, oldest first. Rows carry the raw TokenHash; callers
// presenting these sessions to users must project them onto safe metadata
// only.
func (r *UserRepository) GetLiveUserSessions(c context.Context, userID int) ([]*uModel.UserSession, error) {
	var sessions []*uModel.UserSession
	now := time.Now().UTC()
	err := r.db.WithContext(c).
		Where("user_id = ? AND revoked_at IS NULL AND expires_at > ?", userID, now).
		Order("created_at ASC").
		Find(&sessions).Error
	return sessions, err
}

// RevokeAllUserSessions revokes all sessions for a user (logout from all devices)
func (r *UserRepository) RevokeAllUserSessions(c context.Context, userID int) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if _, err := lockUserSessionWrites(tx, userID); err != nil {
			return err
		}
		return tx.Model(&uModel.UserSession{}).Where("user_id = ? AND revoked_at IS NULL", userID).Update("revoked_at", time.Now().UTC()).Error
	})
}

// RevokeOtherUserSessions revokes every live session of a user except the
// given one. Used after a password change so the device that performed the
// change keeps its session while every other refresh family dies.
func (r *UserRepository) RevokeOtherUserSessions(c context.Context, userID int, keepSessionID string) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if _, err := lockUserSessionWrites(tx, userID); err != nil {
			return err
		}
		return tx.Model(&uModel.UserSession{}).
			Where("user_id = ? AND revoked_at IS NULL AND id <> ?", userID, keepSessionID).
			Update("revoked_at", time.Now().UTC()).Error
	})
}

func (r *UserRepository) RevokeOtherUserSessionFamilies(c context.Context, userID int, keepFamilyID string) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if _, err := lockUserSessionWrites(tx, userID); err != nil {
			return err
		}
		return tx.Model(&uModel.UserSession{}).
			Where("user_id = ? AND revoked_at IS NULL AND family_id <> ?", userID, keepFamilyID).
			Update("revoked_at", time.Now().UTC()).Error
	})
}

// CleanupExpiredSessions removes expired and revoked sessions
func (r *UserRepository) CleanupExpiredSessions(c context.Context) error {
	now := time.Now().UTC()
	return r.db.WithContext(c).Where("expires_at < ? OR revoked_at IS NOT NULL", now).Delete(&uModel.UserSession{}).Error
}
