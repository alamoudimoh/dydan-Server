package storage

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
)

// maxLocalSignatureHorizon is the largest expiry a local signed URL may
// claim, regardless of the minted TTL. It bounds the damage of a leaked
// signing key (an attacker cannot mint decade-long credentials) and rejects
// absurd or overflow-prone expiry timestamps outright. It matches the hard
// SigV4 ceiling so both credential families share one bound.
const maxLocalSignatureHorizon = config.MaxSignedURLTTL

// URLSignerLocal signs locally-served media URLs with HMAC-SHA256.
//
// The signing key is the storage-specific media secret
// (config.Storage.MediaSecret), never the raw JWT secret. Before phase 6 the
// JWT secret was used directly; URLs signed that way keep working only while
// cfg.AllowLegacyMediaSignatures is true, and only for their natural (<= 7d)
// lifetime — legacy acceptance is a bounded upgrade window, not a second
// permanent credential family.
type URLSignerLocal struct {
	key []byte
	// legacyKey holds the pre-phase-6 key (raw JWT secret) when legacy
	// acceptance is enabled and it differs from the current key. nil otherwise.
	legacyKey []byte
	ttl       time.Duration
}

func NewURLSignerLocal(cfg *config.Config) *URLSignerLocal {
	s := &URLSignerLocal{
		key: []byte(cfg.Storage.MediaSecret),
		ttl: cfg.Storage.SignedURLTTL,
	}
	if s.ttl <= 0 || s.ttl > config.MaxSignedURLTTL {
		// Defensive: validation enforces this at startup, but the signer is
		// also constructed directly in tests.
		s.ttl = config.MaxSignedURLTTL
	}
	if cfg.Storage.AllowLegacyMediaSignatures && len(cfg.Jwt.Secret) > 0 && !hmac.Equal([]byte(cfg.Jwt.Secret), s.key) {
		s.legacyKey = []byte(cfg.Jwt.Secret)
	}
	return s
}

func (s *URLSignerLocal) Sign(rawPath string) (string, error) {
	expires := time.Now().UTC().Add(s.ttl).Unix()
	sig := s.sign(rawPath, expires)
	values := url.Values{}
	values.Set("expires", strconv.FormatInt(expires, 10))
	values.Set("sig", sig)
	return fmt.Sprintf("%s?%s", rawPath, values.Encode()), nil
}

func (s *URLSignerLocal) sign(path string, expires int64) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(fmt.Sprintf("%s:%d", path, expires)))
	return hex.EncodeToString(mac.Sum(nil))
}

func (s *URLSignerLocal) isValidWith(key []byte, path string, expires int64, providedSig string) bool {
	mac := hmac.New(sha256.New, key)
	mac.Write([]byte(fmt.Sprintf("%s:%d", path, expires)))
	return hmac.Equal([]byte(hex.EncodeToString(mac.Sum(nil))), []byte(providedSig))
}

// signatureHorizonSkew tolerates small clock differences between replicas
// (and NTP backward steps) when URLs minted at exactly the maximum TTL are
// verified on a peer whose clock is marginally behind the minter's.
const signatureHorizonSkew = 5 * time.Minute

// IsValid verifies a local signed URL. The expiry check uses direct integer
// comparisons against now so huge (near-2^63) timestamps cannot wrap around;
// expiries further than maxLocalSignatureHorizon (+ skew) in the future are
// rejected even though they would require the signing key to exist in the
// first place.
func (s *URLSignerLocal) IsValid(rawPath string, query url.Values) bool {
	expiresStr := query.Get("expires")
	providedSig := query.Get("sig")
	if expiresStr == "" || providedSig == "" {
		return false
	}
	expires, err := strconv.ParseInt(expiresStr, 10, 64)
	if err != nil {
		return false
	}
	now := time.Now().Unix()
	if expires <= now {
		return false
	}
	if expires > now+int64((maxLocalSignatureHorizon+signatureHorizonSkew)/time.Second) {
		return false
	}
	if s.isValidWith(s.key, rawPath, expires, providedSig) {
		return true
	}
	if s.legacyKey != nil && s.isValidWith(s.legacyKey, rawPath, expires, providedSig) {
		return true
	}
	return false
}

func (s *URLSignerLocal) SignIfLocal(path string) string {
	if path == "" {
		return path
	}
	if strings.HasPrefix(path, "http://") || strings.HasPrefix(path, "https://") {
		return path
	}

	return path
}

func (s *URLSignerLocal) SignAndGetPublicURL(rawPath string) (string, error) {
	// For local storage, we can return a signed URL since it's not publicly accessible.
	return s.Sign(rawPath)
}
