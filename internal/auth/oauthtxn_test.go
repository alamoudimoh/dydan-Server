package auth

import (
	"strings"
	"testing"
	"time"
)

// RFC 7636 appendix B test vector for S256.
func TestPKCES256ChallengeKnownVector(t *testing.T) {
	verifier := "dBjftJeZ4CVP-mB92K27uhbUJU1p1r_wW1gFWFOEjXk"
	want := "E9Melhoa2OwvFrEMTJguCHaoeK1t8URWbuGJSstw-cM"
	if got := PKCES256Challenge(verifier); got != want {
		t.Fatalf("challenge mismatch: got %q want %q", got, want)
	}
}

func TestTransactionStoreSingleUseAndReplayRejected(t *testing.T) {
	store := NewOAuthTransactionStore()

	txn, err := store.Start("https://example.com/auth/oauth2")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	if len(txn.State) < 32 {
		t.Fatalf("state too weak (%d chars): %q", len(txn.State), txn.State)
	}
	if txn.CodeVerifier == "" || txn.CodeChallenge == "" {
		t.Fatal("PKCE verifier/challenge missing from transaction")
	}
	if PKCES256Challenge(txn.CodeVerifier) != txn.CodeChallenge {
		t.Fatal("code_challenge is not the S256 of the verifier")
	}

	consumed, err := store.Consume(txn.State)
	if err != nil {
		t.Fatalf("first consume should succeed: %v", err)
	}
	if consumed.CodeVerifier != txn.CodeVerifier || consumed.RedirectURI != "https://example.com/auth/oauth2" {
		t.Fatal("consumed transaction lost its binding data")
	}

	// Replay must be rejected.
	if _, err := store.Consume(txn.State); err == nil {
		t.Fatal("replayed state was accepted")
	}

	// Unknown / empty states are rejected.
	if _, err := store.Consume("forged-state"); err == nil {
		t.Fatal("forged state accepted")
	}
	if _, err := store.Consume(""); err == nil {
		t.Fatal("empty state accepted")
	}
}

func TestTransactionStoreExpiry(t *testing.T) {
	store := NewOAuthTransactionStoreWithTTL(5 * time.Millisecond)

	txn, err := store.Start("https://example.com/auth/oauth2")
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	time.Sleep(15 * time.Millisecond)

	if _, err := store.Consume(txn.State); err != ErrExpiredOAuthState {
		t.Fatalf("expected ErrExpiredOAuthState, got %v", err)
	}
	// Expired transaction is consumed too: no further redemption.
	if _, err := store.Consume(txn.State); err == nil {
		t.Fatal("expired transaction redeemable twice")
	}
}

func TestTransactionStoreStatesAreUniqueAndStrong(t *testing.T) {
	store := NewOAuthTransactionStore()
	seen := map[string]bool{}
	for i := 0; i < 100; i++ {
		txn, err := store.Start("")
		if err != nil {
			t.Fatalf("Start failed: %v", err)
		}
		if seen[txn.State] {
			t.Fatalf("duplicate state generated: %s", txn.State)
		}
		seen[txn.State] = true
		if strings.ContainsAny(txn.State, "+/=") {
			t.Fatalf("state not URL-safe: %s", txn.State)
		}
	}
}
