package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
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
	RefreshToken       string    `json:"refresh_token"`
	AccessTokenExpiry  time.Time `json:"access_token_expiry"`
	RefreshTokenExpiry time.Time `json:"refresh_token_expiry"`
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

// maxRotationContentionRetries bounds how many times handleAlreadyUsedSession
// re-reads the family head under extreme concurrency before failing closed.
const maxRotationContentionRetries = 8

// rotationContentionBackoff is the pause between re-checks while another
// refresher finishes persisting its successor session.
const rotationContentionBackoff = 10 * time.Millisecond

// GenerateTokens generates both access and refresh tokens for a user
func (s *TokenService) GenerateTokens(ctx context.Context, user *uModel.UserDetails) (*TokenResponse, error) {
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

	if err := s.userRepo.CreateUserSession(ctx, userSession); err != nil {
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
// can consume a given token. A second request presenting the same token is
// either (a) a benign concurrent refresh — e.g. two browser tabs sharing the
// same HttpOnly cookie — in which case rotation continues from the newest
// session of the family, or (b) replay of a rotated-away token, which revokes
// the entire family. Case (a) is distinguished from (b) by the grace window:
// a successor session minted within refreshGraceWindow of this token's use
// proves a legitimate concurrent refresher got there first.
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

	// Atomically claim the single-use rotation slot for this token.
	won, err := s.userRepo.MarkSessionUsedIfUnused(ctx, session.ID)
	if err != nil {
		return nil, fmt.Errorf("failed to mark token as used: %w", err)
	}
	if !won {
		// Another request claimed it between our read and write; approximate
		// its consumption time with now for the grace-window comparison.
		session.UsedAt = &[]time.Time{time.Now().UTC()}[0]
		return s.handleAlreadyUsedSession(ctx, session)
	}

	return s.rotateFromSession(ctx, session)
}

// handleAlreadyUsedSession classifies a second presentation of a consumed
// refresh token as either benign concurrency or theft-and-replay.
//
// The grace decision is anchored to the token's ORIGINAL consumption time
// (session.UsedAt, persisted atomically at first use), not to the age of the
// family head. This is what stops replay-extension attacks: an attacker who
// replays inside the window can mint successors of their own (which move the
// head forward), but every further replay is still measured against the same
// UsedAt timestamp, so the free pass expires exactly refreshGraceWindow after
// legitimate first use no matter how much traffic the attacker generates.
func (s *TokenService) handleAlreadyUsedSession(ctx context.Context, session *uModel.UserSession) (*TokenResponse, error) {
	// Replay after the grace period is theft regardless of how fresh the
	// family head looks — an attacker replaying earlier may have advanced it.
	if time.Now().UTC().After(session.UsedAt.Add(s.refreshGraceWindow)) {
		_ = s.userRepo.RevokeSessionFamily(ctx, session.FamilyID)
		return nil, fmt.Errorf("token reuse detected - please login again")
	}

	for attempt := 0; attempt < maxRotationContentionRetries; attempt++ {
		latest, err := s.userRepo.GetLatestFamilySession(ctx, session.FamilyID)
		if err != nil {
			// No live session left in the family at all: stale replay.
			_ = s.userRepo.RevokeSessionFamily(ctx, session.FamilyID)
			return nil, fmt.Errorf("token reuse detected - please login again")
		}

		// A successor must have been minted because someone else legitimately
		// consumed this token moments ago. A family head older than the grace
		// window relative to this token's use means a rotated-away token is
		// being replayed — steal response, kill the family.
		if latest.CreatedAt.Before(session.UsedAt.Add(-s.refreshGraceWindow)) {
			_ = s.userRepo.RevokeSessionFamily(ctx, session.FamilyID)
			return nil, fmt.Errorf("token reuse detected - please login again")
		}

		if latest.ID == session.ID || latest.UsedAt != nil {
			// The winning refresher is between claiming its rotation slot and
			// persisting the successor. Give it a moment, then look again.
			time.Sleep(rotationContentionBackoff)
			continue
		}

		won, err := s.userRepo.MarkSessionUsedIfUnused(ctx, latest.ID)
		if err != nil {
			return nil, fmt.Errorf("failed to mark token as used: %w", err)
		}
		if !won {
			// Another waiting tab claimed the head first; observe its result.
			time.Sleep(rotationContentionBackoff)
			continue
		}
		return s.rotateFromSession(ctx, latest)
	}
	// Sustained contention beyond any realistic tab count: fail closed.
	_ = s.userRepo.RevokeSessionFamily(ctx, session.FamilyID)
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
	refreshTokenExpiry := now.Add(s.refreshTokenExpiry)

	newSession := &uModel.UserSession{
		ID:        uuid.New().String(),
		UserID:    session.UserID,
		TokenHash: s.hashToken(newRefreshToken),
		FamilyID:  session.FamilyID, // Keep same family ID for rotation tracking
		CreatedAt: now,
		ExpiresAt: refreshTokenExpiry,
	}

	if err := s.userRepo.CreateUserSession(ctx, newSession); err != nil {
		return nil, fmt.Errorf("failed to store new refresh token: %w", err)
	}

	accessToken, err := s.mintAccessToken(user, accessTokenTime{
		ttl:      s.accessTokenTime,
		session:  newSession.ID,
		familyID: session.FamilyID,
	})
	if err != nil {
		return nil, err
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

// RevokeToken revokes a specific refresh token
func (s *TokenService) RevokeToken(ctx context.Context, refreshToken string) error {
	tokenHash := s.hashToken(refreshToken)
	session, err := s.userRepo.GetUserSessionByTokenHash(ctx, tokenHash)
	if err != nil {
		return nil // Token not found - already invalid
	}
	return s.userRepo.RevokeSession(ctx, session.ID)
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
