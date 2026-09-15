package auth

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strconv"
	"time"

	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
)

var ErrInvalidAccessSecurityState = errors.New("invalid access token security state")

// ValidateAccessClaims reloads all mutable authentication state from the
// database. It is deliberately uncached so a committed revocation is visible
// to every replica and remains effective after restart.
func ValidateAccessClaims(ctx context.Context, repo *uRepo.UserRepository, claims map[string]interface{}) (*uModel.UserDetails, error) {
	expiresAt, ok := integerClaim(claims["exp"])
	if !ok || time.Now().UTC().Unix() >= int64(expiresAt) {
		return nil, ErrInvalidAccessSecurityState
	}
	return validateMutableAccessState(ctx, repo, claims)
}

// ValidateRealtimeSession re-checks the authoritative authorization state of
// an established realtime connection (SSE/WebSocket) using the claims
// captured at its handshake. Unlike ValidateAccessClaims it deliberately
// ignores the handshake JWT's "exp": ongoing authorization for a long-lived
// connection is continuous authoritative state — account enabled, credential
// version unchanged, and the exact session row still live — so a healthy
// session is not cut off merely because its original short-lived JWT aged out.
func ValidateRealtimeSession(ctx context.Context, repo *uRepo.UserRepository, claims map[string]interface{}) (*uModel.UserDetails, error) {
	return validateMutableAccessState(ctx, repo, claims)
}

// validateMutableAccessState enforces every DB-backed condition shared by the
// request-time and realtime-time validators.
func validateMutableAccessState(ctx context.Context, repo *uRepo.UserRepository, claims map[string]interface{}) (*uModel.UserDetails, error) {
	username, ok := claims[identityKey].(string)
	if !ok || username == "" {
		return nil, ErrInvalidAccessSecurityState
	}
	credentialVersion, ok := integerClaim(claims["cv"])
	if !ok {
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
	if sessionID != "" {
		// Session-granular enforcement: the token is bound to one session row,
		// so only that exact row may authorize it. Revoking a family stamps
		// every row in it, so a live row still proves family liveness, while a
		// single-session revocation now kills this token without touching any
		// sibling session. Same DB query count as the previous family probe.
		live, err := repo.IsUserSessionLive(ctx, user.ID, sessionID)
		if err != nil || !live {
			return nil, ErrInvalidAccessSecurityState
		}
	} else if familyID != "" {
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
