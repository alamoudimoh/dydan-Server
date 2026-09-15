package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"gorm.io/gorm"
)

// backdateTokenUse rewrites used_at for every session row behind refreshToken
// (exactly one). The grace-window decision anchors on this persisted
// timestamp, so tests travel in time deterministically instead of sleeping.
func backdateTokenUse(t *testing.T, svc *TokenService, db *gorm.DB, refreshToken string, age time.Duration) {
	t.Helper()
	res := db.Model(&uModel.UserSession{}).
		Where("token_hash = ?", svc.hashToken(refreshToken)).
		Update("used_at", time.Now().UTC().Add(-age))
	if res.Error != nil {
		t.Fatalf("backdate used_at: %v", res.Error)
	}
	if res.RowsAffected != 1 {
		t.Fatalf("backdate used_at: expected 1 row, got %d", res.RowsAffected)
	}
}

func TestReplayWithinGraceWindowCannotMintDescendant(t *testing.T) {
	svc, db, user := newTokenServiceHarness(t)
	ctx := context.Background()

	first, err := svc.GenerateTokens(ctx, &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if _, err := svc.RefreshTokens(ctx, first.RefreshToken); err != nil {
		t.Fatalf("legitimate rotation: %v", err)
	}

	// A few seconds of tab skew, still far inside the 90s window.
	backdateTokenUse(t, svc, db, first.RefreshToken, 5*time.Second)

	if _, err := svc.RefreshTokens(ctx, first.RefreshToken); !errors.Is(err, ErrRefreshRetry) {
		t.Fatalf("replay inside grace window must only request retry: %v", err)
	}
}

// TestReplayCannotExtendGraceIndefinitely is the core Phase 3 regression:
// under Phase 2 semantics an attacker replaying an already-rotated token
// during the window minted successors that kept moving the family head
// forward, and each later replay compared against that fresh head — letting
// replayed tokens live forever. The grace decision now anchors on the
// original used_at, so once it passes, replay is theft even though the head
// looks brand new.
func TestReplayCannotExtendGraceIndefinitely(t *testing.T) {
	svc, db, user := newTokenServiceHarness(t)
	ctx := context.Background()

	first, err := svc.GenerateTokens(ctx, &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	family := familyIDOf(t, svc, db, first.RefreshToken)

	// Legitimate use at T0.
	if _, err := svc.RefreshTokens(ctx, first.RefreshToken); err != nil {
		t.Fatalf("legitimate rotation: %v", err)
	}

	// Attacker replay inside the window receives no credentials.
	backdateTokenUse(t, svc, db, first.RefreshToken, 30*time.Second)
	if _, err := svc.RefreshTokens(ctx, first.RefreshToken); !errors.Is(err, ErrRefreshRetry) {
		t.Fatalf("first in-window replay should only request retry: %v", err)
	}

	// Repeated replay still cannot advance the family head.
	backdateTokenUse(t, svc, db, first.RefreshToken, 80*time.Second)
	if _, err := svc.RefreshTokens(ctx, first.RefreshToken); !errors.Is(err, ErrRefreshRetry) {
		t.Fatalf("second in-window replay should only request retry: %v", err)
	}

	// The window closes on the ORIGINAL consumption time, not on attacker
	// activity. The head was minted moments ago, yet replay must fail.
	backdateTokenUse(t, svc, db, first.RefreshToken, 2*defaultRefreshGraceWindow)
	if _, err := svc.RefreshTokens(ctx, first.RefreshToken); err == nil {
		t.Fatal("replay after original-use + grace must be rejected regardless of fresh head")
	}

	if got := liveFamilySessions(t, db, family); got != 0 {
		t.Fatalf("expected entire family revoked, got %d live sessions", got)
	}

}

// TestRepeatedAttackerReplayIsBounded proves the attacker cannot ride along
// forever while legitimate activity continues: every replay is measured
// against the same fixed anchor.
func TestRepeatedAttackerReplayIsBounded(t *testing.T) {
	svc, db, user := newTokenServiceHarness(t)
	ctx := context.Background()

	first, err := svc.GenerateTokens(ctx, &uModel.UserDetails{User: *user})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	// Legitimate tab refreshes, then keeps refreshing its rotated tokens.
	head := first.RefreshToken
	if _, err := svc.RefreshTokens(ctx, head); err != nil {
		t.Fatalf("legit rotation: %v", err)
	}
	backdateTokenUse(t, svc, db, head, defaultRefreshGraceWindow+time.Second)

	// From here on, ANY replay of the original token revokes the family.
	for i := 0; i < 3; i++ {
		if _, err := svc.RefreshTokens(ctx, first.RefreshToken); err == nil {
			t.Fatalf("replay #%d after grace must fail", i+1)
		}
	}
}
