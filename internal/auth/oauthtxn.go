package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sync"
	"time"
)

// OAuth transaction store: single-use, expiring server-side records binding an
// OAuth2/OIDC state value to its PKCE verifier and the validated redirect URI.
//
// The SPA requests a transaction via POST /api/v1/auth/oauth2/start, builds
// the provider authorize URL from the returned state/code_challenge, and later
// presents the state at the callback. Consume is single-use, so replayed or
// forged states are rejected and abandoned transactions expire.

var (
	ErrUnknownOAuthState = errors.New("unknown or already consumed oauth state")
	ErrExpiredOAuthState = errors.New("oauth state expired")
)

const (
	stateBytes      = 32 // 256-bit state
	verifierBytes   = 64 // 512-bit PKCE verifier pre-encoding
	defaultStateTTL = 10 * time.Minute
)

// OAuthPendingTransaction is one in-flight authorization-code login.
type OAuthPendingTransaction struct {
	State         string
	CodeVerifier  string
	CodeChallenge string
	RedirectURI   string
	CreatedAt     time.Time
	ExpiresAt     time.Time
}

func randomToken(n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

// PKCES256Challenge derives the S256 code_challenge from a verifier.
func PKCES256Challenge(verifier string) string {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

type OAuthTransactionStore struct {
	mu      sync.Mutex
	ttl     time.Duration
	pending map[string]OAuthPendingTransaction
}

func NewOAuthTransactionStore() *OAuthTransactionStore {
	return NewOAuthTransactionStoreWithTTL(defaultStateTTL)
}

func NewOAuthTransactionStoreWithTTL(ttl time.Duration) *OAuthTransactionStore {
	if ttl <= 0 {
		ttl = defaultStateTTL
	}
	return &OAuthTransactionStore{
		ttl:     ttl,
		pending: make(map[string]OAuthPendingTransaction),
	}
}

// Start creates a new pending transaction bound to the given redirect URI.
// Expired entries are swept opportunistically so no background job is needed.
func (s *OAuthTransactionStore) Start(redirectURI string) (*OAuthPendingTransaction, error) {
	state, err := randomToken(stateBytes)
	if err != nil {
		return nil, err
	}
	verifier, err := randomToken(verifierBytes)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC()
	txn := OAuthPendingTransaction{
		State:         state,
		CodeVerifier:  verifier,
		CodeChallenge: PKCES256Challenge(verifier),
		RedirectURI:   redirectURI,
		CreatedAt:     now,
		ExpiresAt:     now.Add(s.ttl),
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sweepLocked(now)
	s.pending[state] = txn
	// Return a copy so callers cannot mutate stored state.
	copied := txn
	return &copied, nil
}

// Consume validates and removes a pending transaction atomically. A state can
// only be redeemed once: replays fail with ErrUnknownOAuthState, expired
// transactions with ErrExpiredOAuthState.
func (s *OAuthTransactionStore) Consume(state string) (*OAuthPendingTransaction, error) {
	if state == "" {
		return nil, ErrUnknownOAuthState
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	txn, ok := s.pending[state]
	if !ok {
		return nil, ErrUnknownOAuthState
	}
	delete(s.pending, state)
	now := time.Now().UTC()
	if now.After(txn.ExpiresAt) {
		return nil, ErrExpiredOAuthState
	}
	copied := txn
	return &copied, nil
}

func (s *OAuthTransactionStore) sweepLocked(now time.Time) {
	for state, txn := range s.pending {
		if now.After(txn.ExpiresAt) {
			delete(s.pending, state)
		}
	}
}

// Len reports the number of pending transactions (test/introspection aid).
func (s *OAuthTransactionStore) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}
