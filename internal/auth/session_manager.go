package auth

import (
	"context"
	"time"
)

// SessionInfo is the user-facing projection of one login (a rotation family).
// It deliberately carries credential-free metadata only: no refresh-token
// values, no token hashes, no bearer JWTs ever leave the database through
// these structs.
type SessionInfo struct {
	// ID identifies the rotation family ("device session") and doubles as the
	// revocation handle for DELETE /auth/sessions/:id.
	ID string `json:"id"`
	// CreatedAt is when this login originally happened.
	CreatedAt time.Time `json:"createdAt"`
	// LastActiveAt is when the newest rotation in this family was minted,
	// i.e. the last time the client refreshed.
	LastActiveAt time.Time `json:"lastActiveAt"`
	// ExpiresAt is when the family's current refresh credential expires.
	ExpiresAt time.Time `json:"expiresAt"`
	// Current marks the session the caller is authenticated with right now.
	Current bool `json:"current"`
}

// ListUserSessions aggregates the user's live refresh sessions into one entry
// per rotation family. currentSessionID (the caller's own session row, from
// the access-token claims or the refresh cookie) marks the Current entry and
// may be empty for legacy tokens without lineage claims.
func (s *TokenService) ListUserSessions(ctx context.Context, userID int, currentSessionID string) ([]SessionInfo, error) {
	rows, err := s.userRepo.GetLiveUserSessions(ctx, userID)
	if err != nil {
		return nil, err
	}

	type familyAgg struct {
		createdAt    time.Time
		lastActiveAt time.Time
		expiresAt    time.Time
		current      bool
	}
	families := map[string]*familyAgg{}
	var order []string

	for _, row := range rows {
		if row.RevokedAt != nil {
			continue
		}
		agg, seen := families[row.FamilyID]
		if !seen {
			agg = &familyAgg{createdAt: row.CreatedAt, lastActiveAt: row.CreatedAt, expiresAt: row.ExpiresAt}
			families[row.FamilyID] = agg
			order = append(order, row.FamilyID)
		}
		if row.CreatedAt.Before(agg.createdAt) {
			agg.createdAt = row.CreatedAt
		}
		if row.CreatedAt.After(agg.lastActiveAt) {
			agg.lastActiveAt = row.CreatedAt
		}
		if row.ExpiresAt.After(agg.expiresAt) {
			agg.expiresAt = row.ExpiresAt
		}
	}

	var currentFamily string
	if currentSessionID != "" {
		for _, row := range rows {
			if row.ID == currentSessionID {
				currentFamily = row.FamilyID
				break
			}
		}
	}

	out := make([]SessionInfo, 0, len(order))
	for _, fid := range order {
		agg := families[fid]
		out = append(out, SessionInfo{
			ID:           fid,
			CreatedAt:    agg.createdAt.UTC(),
			LastActiveAt: agg.lastActiveAt.UTC(),
			ExpiresAt:    agg.expiresAt.UTC(),
			Current:      fid == currentFamily,
		})
	}
	return out, nil
}

// RevokeUserSessionFamily revokes one rotation family after verifying it
// belongs to userID, keeping authorization boundaries between users intact.
// Returns false when no such live family exists for this user.
func (s *TokenService) RevokeUserSessionFamily(ctx context.Context, userID int, familyID string) (bool, error) {
	if familyID == "" {
		return false, nil
	}
	return s.userRepo.RevokeUserSessionFamily(ctx, userID, familyID)
}

// RevokeUserSessionByID revokes exactly one refresh session row after
// verifying ownership. The session's already-issued access JWTs stop working
// immediately (validation checks the exact row), while sibling sessions in
// the same rotation family remain live. Returns false when no such live
// session exists for this user.
func (s *TokenService) RevokeUserSessionByID(ctx context.Context, userID int, sessionID string) (bool, error) {
	if sessionID == "" {
		return false, nil
	}
	return s.userRepo.RevokeUserSessionByID(ctx, userID, sessionID)
}

// RevokeOtherUserFamilies revokes every live family of the user except the
// identified current family.
func (s *TokenService) RevokeOtherUserFamilies(ctx context.Context, userID int, keepFamilyID string) error {
	return s.userRepo.RevokeOtherUserSessionFamilies(ctx, userID, keepFamilyID)
}

// SessionRefForToken resolves both the session row and its family behind a
// raw refresh token, or empty strings when unknown.
func (s *TokenService) SessionRefForToken(ctx context.Context, refreshToken string) (sessionID, familyID string) {
	if refreshToken == "" {
		return "", ""
	}
	session, err := s.userRepo.GetUserSessionByTokenHash(ctx, s.hashToken(refreshToken))
	if err != nil || session == nil {
		return "", ""
	}
	return session.ID, session.FamilyID
}
