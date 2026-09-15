package storage

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	jwtstd "github.com/golang-jwt/jwt/v4"
)

// mediaTestConfig builds a config as LoadConfigE would produce it after
// resolveMediaSigningKey: Storage.MediaSecret populated, legacy acceptance
// configurable.
func mediaTestConfig(jwtSecret, mediaSecret string, allowLegacy bool) *config.Config {
	return &config.Config{
		Jwt: config.JwtConfig{Secret: jwtSecret},
		Storage: config.StorageConfig{
			MediaSecret:                mediaSecret,
			SignedURLTTL:               config.MaxSignedURLTTL,
			AllowLegacyMediaSignatures: allowLegacy,
		},
	}
}

// TestMediaSigningKeyIsDomainSeparatedFromJWTKey proves the zero-config
// derived media key is distinct from the JWT secret and stable across
// derivations (same secret -> same key), so restarts don't invalidate URLs.
func TestMediaSigningKeyIsDomainSeparatedFromJWTKey(t *testing.T) {
	const jwtSecret = "jwt-master-secret-used-for-sessions-only"
	a, err := config.DeriveMediaKeyHex([]byte(jwtSecret))
	if err != nil {
		t.Fatal(err)
	}
	b, err := config.DeriveMediaKeyHex([]byte(jwtSecret))
	if err != nil {
		t.Fatal(err)
	}
	if a != b {
		t.Fatal("derived media key must be deterministic for the same JWT secret")
	}
	if a == jwtSecret {
		t.Fatal("derived media key must differ from the raw JWT secret")
	}
	if strings.Contains(jwtSecret, a) || strings.Contains(a, jwtSecret) {
		t.Fatal("derived media key must not embed the JWT secret verbatim")
	}
	other, _ := config.DeriveMediaKeyHex([]byte(jwtSecret + "x"))
	if other == a {
		t.Fatal("different JWT secrets must derive different media keys")
	}
}

func TestDydanDerivedMediaKeyAcceptsValidLegacyDaydanURLs(t *testing.T) {
	const jwtSecret = "dydan-media-transition-secret-0123456789"
	cfg := mediaTestConfig(jwtSecret, "", true)
	if err := config.ValidateMediaKeyForTest(cfg); err != nil {
		t.Fatal(err)
	}
	canonical := NewURLSignerLocal(cfg)
	canonicalURL, err := canonical.Sign("users/7/photo.png")
	if err != nil {
		t.Fatal(err)
	}
	canonicalPath, canonicalQuery := parseSignedURL(t, canonicalURL)
	if !canonical.IsValid(canonicalPath.Path, canonicalQuery) {
		t.Fatal("canonical Dydan URL did not verify")
	}

	legacy := &URLSignerLocal{key: []byte(cfg.Storage.LegacyMediaSecret), ttl: time.Hour}
	legacyURL, err := legacy.Sign("users/7/photo.png")
	if err != nil {
		t.Fatal(err)
	}
	legacyPath, legacyQuery := parseSignedURL(t, legacyURL)
	if !canonical.IsValid(legacyPath.Path, legacyQuery) {
		t.Fatal("valid legacy DayDan URL did not verify")
	}
	if canonical.IsValid("users/7/tampered.png", canonicalQuery) || canonical.IsValid("users/7/tampered.png", legacyQuery) {
		t.Fatal("tampered canonical or legacy URL verified")
	}
}

// TestJWTSignedMediaURLsRequireLegacyWindow proves the pre-phase-6 scheme
// (HMAC with the raw JWT secret) is rejected once the flag is off.
func TestJWTSignedMediaURLsRequireLegacyWindow(t *testing.T) {
	const jwtSecret = "legacy-jwt-secret"
	const mediaSecret = "independent-media-secret-0123456789abcdef"

	// Craft a URL signed exactly like phase <= 5 did: raw JWT-secret HMAC.
	legacySigner := &URLSignerLocal{key: []byte(jwtSecret), ttl: time.Hour}
	signed, err := legacySigner.Sign("users/9/photo.png")
	if err != nil {
		t.Fatal(err)
	}
	parsed, query := parseSignedURL(t, signed)

	rejecting := NewURLSignerLocal(mediaTestConfig(jwtSecret, mediaSecret, false))
	if rejecting.IsValid(parsed.Path, query) {
		t.Fatal("JWT-key-signed media URL must be rejected when legacy window is disabled")
	}
	accepting := NewURLSignerLocal(mediaTestConfig(jwtSecret, mediaSecret, true))
	if !accepting.IsValid(parsed.Path, query) {
		t.Fatal("legacy-window signer must still accept pre-upgrade URLs")
	}
	// The bounded window must not accept legacy signatures on paths that were
	// never signed: acceptance is per-URL, not a general bypass.
	parsed2, query2 := parseSignedURL(t, signed)
	if accepting.IsValid(strings.Replace(parsed2.Path, "photo", "other", 1), query2) {
		t.Fatal("legacy window must not validate tampered paths")
	}
}

// TestMediaSignatureCannotMintJWT proves a credential from the media domain
// cannot be replayed into the session domain: an HS256 token whose signature
// was produced by the media signing material fails verification against the
// JWT secret (the only key gin-jwt ever uses).
func TestMediaSignatureCannotMintJWT(t *testing.T) {
	mediaSigner := NewURLSignerLocal(mediaTestConfig("session-secret-abcdef", "media-secret-0123456789abcdef", true))
	signedURL, err := mediaSigner.Sign("profiles/1/x.jpg")
	if err != nil {
		t.Fatal(err)
	}
	parsed, query := parseSignedURL(t, signedURL)
	mediaSig := query.Get("sig")

	claims := jwtstd.MapClaims{"user_id": 1, "exp": time.Now().Add(time.Hour).Unix()}

	// Attempt 1: use the raw media signature bytes as an HS256 signature over
	// a standard claims payload. Build the compact JWS manually with the
	// correct header/payload segments but the media credential as signature.
	header := map[string]string{"alg": "HS256", "typ": "JWT"}
	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	claimsJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatal(err)
	}
	forged := base64.RawURLEncoding.EncodeToString(headerJSON) + "." +
		base64.RawURLEncoding.EncodeToString(claimsJSON) + "." + mediaSig
	if _, err := jwtstd.Parse(forged, func(tok *jwtstd.Token) (interface{}, error) {
		return []byte("session-secret-abcdef"), nil
	}); err == nil {
		t.Fatal("media credential spliced into a JWT must fail JWT verification")
	}

	// Attempt 2: sign a full HS256 JWT with the media secret itself and verify
	// with the JWT secret — the exact check the auth middleware performs.
	token := jwtstd.NewWithClaims(jwtstd.SigningMethodHS256, claims)
	mintedWithMediaKey, err := token.SignedString([]byte(mediaSigner.key))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := jwtstd.Parse(mintedWithMediaKey, func(tok *jwtstd.Token) (interface{}, error) {
		return []byte("session-secret-abcdef"), nil
	}); err == nil {
		t.Fatal("JWT minted with the media key must not verify against the JWT key")
	}
	_ = parsed
}

// TestJWTSignatureCannotServeMedia proves the inverse direction: an HS256
// JWT signature value cannot be used as a media URL signature.
func TestJWTSignatureCannotServeMedia(t *testing.T) {
	cfg := mediaTestConfig("session-secret-abcdef", "media-secret-0123456789abcdef", true)
	signer := NewURLSignerLocal(cfg)

	token := jwtstd.NewWithClaims(jwtstd.SigningMethodHS256, jwtstd.MapClaims{"sub": "1"})
	jwtString, err := token.SignedString([]byte(cfg.Jwt.Secret))
	if err != nil {
		t.Fatal(err)
	}
	// The trailing segment of a compact JWS is its signature.
	parts := strings.Split(jwtString, ".")
	jwtSignaturePart := parts[len(parts)-1]

	expires := time.Now().UTC().Add(time.Hour).Unix()
	query := url.Values{}
	query.Set("expires", strconv.FormatInt(expires, 10))
	query.Set("sig", jwtSignaturePart)
	if signer.IsValid("users/1/private.jpg", query) {
		t.Fatal("a JWT signature must not verify as a media signature")
	}
}

// TestRotatingMediaKeyDoesNotRotateJWTSessions proves independence of the two
// rotation domains: mint a JWT "session", rotate the media key, and confirm
// the session credential still verifies while old media URLs do not.
func TestRotatingMediaKeyDoesNotRotateJWTSessions(t *testing.T) {
	cfg := mediaTestConfig("stable-session-secret-0123456789", "original-media-key-0123456789a", false)
	signerBefore := NewURLSignerLocal(cfg)
	oldSignedURL, err := signerBefore.Sign("users/4/attachment.pdf")
	if err != nil {
		t.Fatal(err)
	}
	oldParsed, oldQuery := parseSignedURL(t, oldSignedURL)

	sessionToken, err := jwtstd.NewWithClaims(jwtstd.SigningMethodHS256,
		jwtstd.MapClaims{"id": 4}).SignedString([]byte(cfg.Jwt.Secret))
	if err != nil {
		t.Fatal(err)
	}

	// Rotate ONLY the media key.
	cfg.Storage.MediaSecret = "rotated-media-key-abcdefghijklmnop"
	signerAfter := NewURLSignerLocal(cfg)

	if _, err := jwtstd.Parse(sessionToken, func(tok *jwtstd.Token) (interface{}, error) {
		return []byte(cfg.Jwt.Secret), nil
	}); err != nil {
		t.Fatalf("media-key rotation must not invalidate JWT sessions: %v", err)
	}
	if signerAfter.IsValid(oldParsed.Path, oldQuery) {
		t.Fatal("old media URLs must stop validating after media-key rotation")
	}
	fresh, err := signerAfter.Sign(oldParsed.Path)
	if err != nil {
		t.Fatal(err)
	}
	newParsed, newQuery := parseSignedURL(t, fresh)
	if !signerAfter.IsValid(newParsed.Path, newQuery) {
		t.Fatal("newly minted media URLs must validate under the rotated key")
	}
}

// TestRotatingJWTKeyDoesNotRotateExplicitMediaKey proves that with an
// explicitly configured media key, rotating the JWT secret leaves media URL
// verification untouched — JWT rotation is never implicitly a media rotation.
func TestRotatingJWTKeyDoesNotRotateExplicitMediaKey(t *testing.T) {
	cfg := mediaTestConfig("old-session-secret-012345678", "explicit-media-key-012345678", false)
	signer := NewURLSignerLocal(cfg)
	signed, err := signer.Sign("users/2/img.jpg")
	if err != nil {
		t.Fatal(err)
	}
	parsed, query := parseSignedURL(t, signed)

	cfg.Jwt.Secret = "brand-new-session-secret-01234"
	signerAfterJWTRotation := NewURLSignerLocal(cfg)
	if !signerAfterJWTRotation.IsValid(parsed.Path, query) {
		t.Fatal("JWT-key rotation must not invalidate explicitly-keyed media URLs")
	}
}

// TestVerifierRejectsExcessiveAndMalformedExpiry covers forged/oversized/
// malformed expiry timestamps, including values near int64 overflow.
func TestVerifierRejectsExcessiveAndMalformedExpiry(t *testing.T) {
	signer := NewURLSignerLocal(mediaTestConfig("k", "media-secret-overflow-tests-123456", false))
	path := "users/1/x.jpg"
	now := time.Now().Unix()

	cases := []struct {
		name    string
		expires string
	}{
		{"far future year 2286", "9999999999"},
		{"int64 max", "9223372036854775807"},
		{"just beyond horizon", strconv.FormatInt(now+int64(config.MaxSignedURLTTL/time.Second)+60, 10)},
		{"negative", "-100"},
		{"zero", "0"},
		{"garbage", "not-a-number"},
		{"empty", ""},
		{"float", "1.5"},
	}
	for _, tc := range cases {
		sig := signer.sign(path, now+3600)
		query := url.Values{}
		query.Set("expires", tc.expires)
		query.Set("sig", sig)
		if signer.IsValid(path, query) {
			t.Errorf("%s: expiry %q must be rejected", tc.name, tc.expires)
		}
	}
}

// TestConfigRejectsMediaSecretEqualToJWTSecret pins the W1 fix: a copy-paste
// of the JWT secret into media_secret would silently defeat key separation
// and re-open the legacy scheme forever, so startup must refuse it.
func TestConfigRejectsMediaSecretEqualToJWTSecret(t *testing.T) {
	clearEnv := func() {}
	_ = clearEnv
	cfg := &config.Config{Jwt: config.JwtConfig{Secret: strings.Repeat("j", 32)}}
	cfg.Storage.MediaSecret = cfg.Jwt.Secret
	if err := config.ValidateMediaKeyForTest(cfg); err == nil {
		t.Fatal("media secret equal to the JWT secret must fail validation")
	}
}
