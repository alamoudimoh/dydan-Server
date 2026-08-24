package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	jwt "github.com/appleboy/gin-jwt/v2"
	jwtstd "github.com/golang-jwt/jwt/v4"
	"github.com/google/uuid"
)

// TokenService handles JWT token generation and refresh token management
type TokenService struct {
	userRepo *uRepo.UserRepository
	jwtAuth  *jwt.GinJWTMiddleware
	// sessionTime is the lifetime of legacy /login/legacy access tokens only.
	sessionTime time.Duration
	// accessTokenTime is the lifetime of interactive (browser PWA + native
	// app) access JWTs. Kept deliberately short so a stolen access token
	// cannot outlive logout/session revocation for long; sessions survive via
	// the rotating refresh credential.
	accessTokenTime    time.Duration
	refreshTokenExpiry time.Duration
	jwtSecret          []byte

	// refreshGraceWindow bounds how long after a rotation a concurrent use of
	// the just-rotated token is still treated as benign (multiple browser tabs
	// refreshing at once) rather than as theft-and-replay. Replays that arrive
	// after this window revoke the whole family.
	refreshGraceWindow time.Duration
}

// TokenResponse represents the response containing both access and refresh tokens
type TokenResponse struct {
	AccessToken        string    `json:"access_token"`
	RefreshToken       string    `json:"refresh_token,omitempty"`
	AccessTokenExpiry  time.Time `json:"access_token_expiry"`
	RefreshTokenExpiry time.Time `json:"refresh_token_expiry,omitempty"`
	TokenType          string    `json:"token_type"`

	// Legacy fields for backward compatibility
	Token  string    `json:"token"`  // Same as access_token
	Expire time.Time `json:"expire"` // Same as access_token_expiry
}

// RefreshRequest represents the request to refresh tokens
type RefreshRequest struct {
	RefreshToken string `json:"refresh_token" binding:"required"`
}

// NewTokenService creates a new token service
func NewTokenService(userRepo *uRepo.UserRepository, jwtAuth *jwt.GinJWTMiddleware, cfg *config.Config) *TokenService {
	return &TokenService{
		userRepo:           userRepo,
		jwtAuth:            jwtAuth,
		sessionTime:        cfg.Jwt.SessionTime,
		accessTokenTime:    cfg.Jwt.AccessTokenTime,
		refreshTokenExpiry: cfg.Jwt.MaxRefresh,
		jwtSecret:          []byte(cfg.Jwt.Secret),
		refreshGraceWindow: defaultRefreshGraceWindow,
	}
}

// defaultRefreshGraceWindow covers the realistic skew between two browser
// tabs firing /auth/refresh at the same moment. A replayed token that shows
// up later than this after its successor was minted is treated as theft.
const defaultRefreshGraceWindow = 90 * time.Second

// ErrRefreshRetry means another request has just rotated the presented token.
// No credentials are issued from the consumed token.
var ErrRefreshRetry = errors.New("refresh token already rotated; retry with current token")

// GenerateTokens generates both access and refresh tokens for a user
func (s *TokenService) GenerateTokens(ctx context.Context, user *uModel.UserDetails) (*TokenResponse, error) {
	if user.Disabled {
		return nil, fmt.Errorf("account disabled")
	}
	// Generate refresh token
	refreshToken, err := s.generateRefreshToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate refresh token: %w", err)
	}

	// Create token family ID for rotation tracking
	familyID := uuid.New().String()

	// Hash refresh token for storage
	tokenHash := s.hashToken(refreshToken)

	// Calculate expiry times using config
	now := time.Now().UTC()
	accessTokenExpiry := now.Add(s.accessTokenTime)
	refreshTokenExpiry := now.Add(s.refreshTokenExpiry)

	// Store refresh token in database
	userSession := &uModel.UserSession{
		ID:        uuid.New().String(),
		UserID:    user.ID,
		TokenHash: tokenHash,
		FamilyID:  familyID,
		CreatedAt: now,
		ExpiresAt: refreshTokenExpiry,
	}

	if err := s.userRepo.CreateUserSession(ctx, userSession, user.CredentialVersion); err != nil {
		return nil, fmt.Errorf("failed to store refresh token: %w", err)
	}

	accessToken, err := s.mintAccessToken(user, accessTokenTime{
		ttl:      s.accessTokenTime,
		session:  userSession.ID,
		familyID: familyID,
	})
	if err != nil {
		return nil, err
	}

	return &TokenResponse{
		AccessToken:        accessToken,
		RefreshToken:       refreshToken,
		AccessTokenExpiry:  accessTokenExpiry,
		RefreshTokenExpiry: refreshTokenExpiry,
		TokenType:          "Bearer",
		// Legacy fields for backward compatibility
		Token:  accessToken,
		Expire: accessTokenExpiry,
	}, nil
}

// accessTokenTime carries the minting parameters for an interactive access
// JWT: its TTL plus the refresh-session lineage it was issued against.
type accessTokenTime struct {
	ttl      time.Duration
	session  string
	familyID string
}

// mintAccessToken signs a short-lived HS256 access JWT with the same claims
// shape the gin-jwt middleware expects ("id" identity claim), plus "sid"/
// "fam" so later requests can identify which refresh session (and rotation
// family) the bearer belongs to. This is what lets session-management
// endpoints flag and preserve the caller's current session without exposing
// any credential material. Interactive tokens use jwt.accessTokenTime rather
// than the middleware Timeout, which remains reserved for legacy
// /login/legacy integration tokens.
func (s *TokenService) mintAccessToken(user *uModel.UserDetails, p accessTokenTime) (string, error) {
	now := time.Now().UTC()
	claims := jwtstd.MapClaims{
		identityKey: user.Username,
		"cv":        user.CredentialVersion,
		"orig_iat":  now.Unix(),
		"exp":       now.Add(p.ttl).Unix(),
	}
	if p.session != "" {
		claims["sid"] = p.session
	}
	if p.familyID != "" {
		claims["fam"] = p.familyID
	}
	token := jwtstd.NewWithClaims(jwtstd.SigningMethodHS256, claims)
	signed, err := token.SignedString(s.jwtSecret)
	if err != nil {
		return "", fmt.Errorf("failed to generate access token: %w", err)
	}
	return signed, nil
}

// SessionRefFromClaims extracts the refresh-session/family references embedded
// in an access JWT, or empty strings for tokens that predate them.
func SessionRefFromClaims(claims map[string]interface{}) (sessionID, familyID string) {
	if v, ok := claims["sid"].(string); ok {
		sessionID = v
	}
	if v, ok := claims["fam"].(string); ok {
		familyID = v
	}
	return sessionID, familyID
}

// RefreshTokens validates a refresh token and generates the next token pair
// in its rotation family.
//
// Rotation is serialized by an atomic "mark used" update: only one request
// can consume a given token. Recent duplicate use receives a retryable conflict
// but no credentials; reuse after the grace window revokes the entire family.
func (s *TokenService) RefreshTokens(ctx context.Context, refreshToken string) (*TokenResponse, error) {
	// Hash the provided refresh token
	tokenHash := s.hashToken(refreshToken)

	// Get live session from database (used sessions are included so the
	// reuse path below can tell concurrent refresh from stale replay).
	session, err := s.userRepo.GetUserSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil, fmt.Errorf("invalid or expired refresh token")
	}

	if session.UsedAt != nil {
		return s.handleAlreadyUsedSession(ctx, session)
	}

	return s.rotateFromSession(ctx, session)
}

// handleAlreadyUsedSession classifies a second presentation of a consumed
// refresh token as either retryable concurrency or theft-and-replay. Neither
// path can rotate from the family head or issue an attacker descendant.
func (s *TokenService) handleAlreadyUsedSession(ctx context.Context, session *uModel.UserSession) (*TokenResponse, error) {
	if !time.Now().UTC().After(session.UsedAt.Add(s.refreshGraceWindow)) {
		return nil, ErrRefreshRetry
	}
	_, _ = s.userRepo.RevokeUserSessionFamily(ctx, session.UserID, session.FamilyID)
	return nil, fmt.Errorf("token reuse detected - please login again")
}

// rotateFromSession mints the next access/refresh pair continuing session's
// rotation family. The caller must have already marked session as used.
func (s *TokenService) rotateFromSession(ctx context.Context, session *uModel.UserSession) (*TokenResponse, error) {
	// Get user details for new token generation
	userData, err := s.userRepo.GetUserByID(ctx, session.UserID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user: %w", err)
	}
	if userData.Disabled {
		_, _ = s.userRepo.RevokeUserSessionFamily(ctx, session.UserID, session.FamilyID)
		return nil, fmt.Errorf("account disabled")
	}

	user := &uModel.UserDetails{
		User: *userData,
		// WebhookURL will be nil for refresh tokens - that's ok
	}

	newRefreshToken, err := s.generateRefreshToken()
	if err != nil {
		return nil, fmt.Errorf("failed to generate new refresh token: %w", err)
	}

	now := time.Now().UTC()
	accessTokenExpiry := now.Add(s.accessTokenTime)
	refreshTokenExpiry := session.ExpiresAt

	newSession := &uModel.UserSession{
		ID:        uuid.New().String(),
		UserID:    session.UserID,
		TokenHash: s.hashToken(newRefreshToken),
		FamilyID:  session.FamilyID, // Keep same family ID for rotation tracking
		CreatedAt: now,
		ExpiresAt: refreshTokenExpiry,
	}
	accessToken, err := s.mintAccessToken(user, accessTokenTime{
		ttl:      s.accessTokenTime,
		session:  newSession.ID,
		familyID: session.FamilyID,
	})
	if err != nil {
		return nil, err
	}

	won, err := s.userRepo.RotateUserSession(ctx, session.ID, newSession)
	if err != nil {
		return nil, fmt.Errorf("failed to store new refresh token: %w", err)
	}
	if !won {
		now := time.Now().UTC()
		session.UsedAt = &now
		return s.handleAlreadyUsedSession(ctx, session)
	}

	return &TokenResponse{
		AccessToken:        accessToken,
		RefreshToken:       newRefreshToken,
		AccessTokenExpiry:  accessTokenExpiry,
		RefreshTokenExpiry: refreshTokenExpiry,
		TokenType:          "Bearer",
		// Legacy fields for backward compatibility
		Token:  accessToken,
		Expire: accessTokenExpiry,
	}, nil
}

// RevokeToken revokes the entire login family behind a refresh token.
func (s *TokenService) RevokeToken(ctx context.Context, refreshToken string) error {
	tokenHash := s.hashToken(refreshToken)
	session, err := s.userRepo.GetUserSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil // Token not found - already invalid
	}
	_, err = s.userRepo.RevokeUserSessionFamily(ctx, session.UserID, session.FamilyID)
	return err
}

// RevokeAllUserTokens revokes all tokens for a user (logout from all devices)
func (s *TokenService) RevokeAllUserTokens(ctx context.Context, userID int) error {
	return s.userRepo.RevokeAllUserSessions(ctx, userID)
}

// SessionIDForToken resolves the session row behind a raw refresh token, or
// an empty string when the token is unknown. Used to identify "the caller's
// own session" when applying revocation policies.
func (s *TokenService) SessionIDForToken(ctx context.Context, refreshToken string) string {
	if refreshToken == "" {
		return ""
	}
	session, err := s.userRepo.GetUserSessionByTokenHash(ctx, s.hashToken(refreshToken))
	if err != nil {
		return ""
	}
	return session.ID
}

// RevokeOtherUserSessions revokes every live session of the user except
// keepSessionID. An empty keepSessionID revokes everything.
func (s *TokenService) RevokeOtherUserSessions(ctx context.Context, userID int, keepSessionID string) error {
	if keepSessionID == "" {
		return s.userRepo.RevokeAllUserSessions(ctx, userID)
	}
	return s.userRepo.RevokeOtherUserSessions(ctx, userID, keepSessionID)
}

// RefreshTokenExpiry returns the configured refresh token expiry duration
func (s *TokenService) RefreshTokenExpiry() time.Duration {
	return s.refreshTokenExpiry
}

// generateRefreshToken creates a cryptographically secure random token
func (s *TokenService) generateRefreshToken() (string, error) {
	bytes := make([]byte, 32) // 256-bit token
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}

// hashToken creates a SHA-256 hash of the token for database storage
func (s *TokenService) hashToken(token string) string {
	hash := sha256.Sum256([]byte(token))
	return hex.EncodeToString(hash[:])
}
