package auth

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
)

var ErrInvalidAccessSecurityState = errors.New("invalid access token security state")

// ValidateAccessClaims reloads all mutable authentication state from the
// database. It is deliberately uncached so a committed revocation is visible
// to every replica and remains effective after restart.
func ValidateAccessClaims(ctx context.Context, repo *uRepo.UserRepository, claims map[string]interface{}) (*uModel.UserDetails, error) {
	username, ok := claims[identityKey].(string)
	if !ok || username == "" {
		return nil, ErrInvalidAccessSecurityState
	}
	credentialVersion, ok := integerClaim(claims["cv"])
	if !ok {
		return nil, ErrInvalidAccessSecurityState
	}
	expiresAt, ok := integerClaim(claims["exp"])
	if !ok || time.Now().UTC().Unix() >= int64(expiresAt) {
		return nil, ErrInvalidAccessSecurityState
	}
	user, err := repo.GetUserByUsername(ctx, username)
	if err != nil || user.Disabled || user.CredentialVersion != credentialVersion {
		return nil, ErrInvalidAccessSecurityState
	}

	sessionID, familyID := SessionRefFromClaims(claims)
	if (sessionID == "") != (familyID == "") {
		return nil, ErrInvalidAccessSecurityState
	}
	if familyID != "" {
		live, err := repo.IsUserSessionFamilyLive(ctx, user.ID, familyID)
		if err != nil || !live {
			return nil, ErrInvalidAccessSecurityState
		}
	}
	return user, nil
}

func integerClaim(value interface{}) (int, bool) {
	switch v := value.(type) {
	case int:
		return v, true
	case int64:
		return int(v), int64(int(v)) == v
	case float64:
		if math.Trunc(v) == v && v >= math.MinInt && v <= math.MaxInt {
			return int(v), true
		}
	case json.Number:
		parsed, err := strconv.Atoi(v.String())
		if err == nil {
			return parsed, true
		}
	}
	return 0, false
}
