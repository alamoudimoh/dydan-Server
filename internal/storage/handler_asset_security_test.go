package storage

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	"github.com/gin-gonic/gin"
)

// newAssetRouter wires a Handler backed by local storage into a fresh gin
// engine with the same route shape as Routes(), without auth middleware
// (the asset-serving route is unauthenticated by design — signed URLs are
// the credential).
func newAssetRouter(t *testing.T, cfg *config.Config) (*gin.Engine, string, *URLSignerLocal) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	root := t.TempDir()
	uploads := filepath.Join(root, "uploads")
	for _, dir := range []string{
		filepath.Join(uploads, "users", "1"),
		filepath.Join(uploads, "profiles", "7"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(uploads, "users", "1", "secret.jpg"), []byte("PROTECTED"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(uploads, "profiles", "7", "avatar.jpg"), []byte("PUBLIC"), 0o644); err != nil {
		t.Fatal(err)
	}
	if cfg.Storage.BasePath == "" {
		cfg.Storage.BasePath = uploads
	}
	signer := NewURLSignerLocal(cfg)
	h := NewHandler(NewLocalStorage(cfg), nil, nil, nil, signer, cfg)
	r := gin.New()
	r.GET("/api/v1/assets/*filepath", h.AssetHandler)
	return r, uploads, signer
}

func doAssetGet(r *gin.Engine, target string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestProtectedAssetClassificationCannotBeEscaped is the regression suite for
// the phase-6 finding that classification ran on un-canonicalized client
// paths: `profiles/../users/1/x.jpg`-shaped requests were treated as public
// and served without any signature.
func TestProtectedAssetClassificationCannotBeEscaped(t *testing.T) {
	cfg := mediaTestConfig("asset-secret", "asset-media-key-0123456789ab", false)
	r, _, _ := newAssetRouter(t, cfg)

	cases := []struct {
		name   string
		target string
	}{
		{"plain protected path", "/api/v1/assets/users/1/secret.jpg"},
		{"literal dotdot", "/api/v1/assets/profiles/../users/1/secret.jpg"},
		{"encoded separator", "/api/v1/assets/profiles%2F..%2Fusers%2F1%2Fsecret.jpg"},
		{"double encoded", "/api/v1/assets/profiles%252F..%252Fusers%252F1%252Fsecret.jpg"},
		{"encoded dotdot segments", "/api/v1/assets/profiles/%2e%2e/users/1/secret.jpg"},
		{"nested prefix lookalike", "/api/v1/assets/users/1/profiles/x/../../1/secret.jpg"},
		{"duplicate slashes", "/api/v1/assets/profiles//../users/1/secret.jpg"},
		{"trailing dotdot", "/api/v1/assets/profiles/7/..%2f..%2fusers%2F1%2Fsecret.jpg"},
		{"backslash separators", `/api/v1/assets/profiles/..\users\1\secret.jpg`},
		{"absolute-ish path", "/api/v1/assets//etc/passwd"},
	}
	for _, tc := range cases {
		w := doAssetGet(r, tc.target)
		if w.Code == http.StatusOK && w.Body.String() == "PROTECTED" {
			t.Errorf("%s: protected asset must never be served unsigned (got %d)", tc.name, w.Code)
		}
	}

	// Control: legitimate public assets remain reachable.
	if w := doAssetGet(r, "/api/v1/assets/profiles/7/avatar.jpg"); w.Code != http.StatusOK || w.Body.String() != "PUBLIC" {
		t.Fatalf("legitimate public asset broken: %d %s", w.Code, w.Body.String())
	}
}

// TestCanonicalAssetPath unit-tests the canonicalizer directly.
func TestCanonicalAssetPath(t *testing.T) {
	ok := map[string]string{
		"users/1/a.jpg":      "users/1/a.jpg",
		"/users/1/a.jpg":     "users/1/a.jpg",
		"profiles/./a.png":   "profiles/a.png",
		"profiles//x.jpg":      "profiles/x.jpg",
		"uploads/profiles/x":   "uploads/profiles/x",
		"./users/1/a.jpg":      "users/1/a.jpg",
	}
	bad := []string{
		"..",
		"../x",
		"a/../..",
		"a/b/../c.jpg",
		"profiles/7/../../users/1/a.jpg",
		"",
		".",
	}
	for in, want := range ok {
		got, err := canonicalAssetPath(in)
		if err != nil || got != want {
			t.Errorf("canonicalAssetPath(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, in := range bad {
		if got, err := canonicalAssetPath(in); err == nil {
			t.Errorf("canonicalAssetPath(%q) = %q; want error", in, got)
		}
	}
}

// TestSignedAssetAdversarial covers tampered paths, tampered expiries,
// missing signatures, wrong signing keys and expired credentials through the
// full application serving path.
func TestSignedAssetAdversarial(t *testing.T) {
	const mediaSecret = "adversarial-media-key-0123456789"
	cfg := mediaTestConfig("jwt-a", mediaSecret, false)
	r, _, signer := newAssetRouter(t, cfg)

	signed, err := signer.Sign("users/1/secret.jpg")
	if err != nil {
		t.Fatal(err)
	}
	query := func() url.Values { _, q := parseSignedURL(t, signed); return q }()

	// Valid URL serves with derived cache headers.
	w := doAssetGet(r, "/api/v1/assets/"+signed)
	if w.Code != http.StatusOK || w.Body.String() != "PROTECTED" {
		t.Fatalf("valid signed fetch failed: %d %s", w.Code, w.Body.String())
	}

	// Tampered path: signature bound to another key must not authorize this one.
	w = doAssetGet(r, "/api/v1/assets/users/1/other.jpg?"+query.Encode())
	if w.Code != http.StatusForbidden {
		t.Fatalf("tampered path must be rejected, got %d", w.Code)
	}

	// Missing signature / missing expiry.
	w = doAssetGet(r, "/api/v1/assets/users/1/secret.jpg")
	if w.Code != http.StatusForbidden {
		t.Fatalf("unsigned request must be rejected, got %d", w.Code)
	}
	qNoSig := url.Values{"expires": query["expires"]}
	w = doAssetGet(r, "/api/v1/assets/users/1/secret.jpg?"+qNoSig.Encode())
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing sig must be rejected, got %d", w.Code)
	}
	qNoExp := url.Values{"sig": query["sig"]}
	w = doAssetGet(r, "/api/v1/assets/users/1/secret.jpg?"+qNoExp.Encode())
	if w.Code != http.StatusForbidden {
		t.Fatalf("missing expires must be rejected, got %d", w.Code)
	}

	// Tampered expiry: re-signing under a different timestamp breaks the MAC.
	qTamperExp := url.Values{
		"expires": []string{strconv.FormatInt(time.Now().Add(48*time.Hour).Unix(), 10)},
		"sig":     query["sig"],
	}
	w = doAssetGet(r, "/api/v1/assets/users/1/secret.jpg?"+qTamperExp.Encode())
	if w.Code != http.StatusForbidden {
		t.Fatalf("tampered expiry must be rejected, got %d", w.Code)
	}

	// Excessive future expiry: validly signed but beyond the hard horizon
	// must be refused even by a verifier holding the current key.
	excessive := time.Now().Add(30 * 24 * time.Hour).Unix()
	qExcess := url.Values{
		"expires": []string{strconv.FormatInt(excessive, 10)},
		"sig":     []string{signer.sign("users/1/secret.jpg", excessive)},
	}
	w = doAssetGet(r, "/api/v1/assets/users/1/secret.jpg?"+qExcess.Encode())
	if w.Code != http.StatusForbidden {
		t.Fatalf("excessive future expiry must be rejected, got %d", w.Code)
	}

	// Wrong signing key: URL minted under a different media secret.
	otherSigner := &URLSignerLocal{key: []byte("completely-different-key"), ttl: time.Hour}
	signedOther, _ := otherSigner.Sign("users/1/secret.jpg")
	w = doAssetGet(r, "/api/v1/assets/"+signedOther)
	if w.Code != http.StatusForbidden {
		t.Fatalf("wrong-key URL must be rejected, got %d", w.Code)
	}

	// Old signing key (legacy scheme) after rotation, legacy window off.
	legacySigner := &URLSignerLocal{key: []byte("jwt-a"), ttl: time.Hour}
	signedLegacy, _ := legacySigner.Sign("users/1/secret.jpg")
	w = doAssetGet(r, "/api/v1/assets/"+signedLegacy)
	if w.Code != http.StatusForbidden {
		t.Fatalf("legacy-key URL must be rejected when window disabled, got %d", w.Code)
	}

	// Already-expired credential served through the application path.
	expiredAt := time.Now().Add(-time.Minute).Unix()
	qExpired := url.Values{
		"expires": []string{strconv.FormatInt(expiredAt, 10)},
		"sig":     []string{signer.sign("users/1/secret.jpg", expiredAt)},
	}
	w = doAssetGet(r, "/api/v1/assets/users/1/secret.jpg?"+qExpired.Encode())
	if w.Code != http.StatusForbidden {
		t.Fatalf("expired credential must be rejected, got %d", w.Code)
	}
}

// TestCacheSemanticsDerivedFromCredentialLifetime proves cache headers can
// never outlive the signed credential for protected assets, while public
// assets keep deliberate long-lived shared caching.
func TestCacheSemanticsDerivedFromCredentialLifetime(t *testing.T) {
	const mediaSecret = "cache-semantics-media-key-01234567"
	cfg := mediaTestConfig("jwt-c", mediaSecret, false)
	r, _, signer := newAssetRouter(t, cfg)

	remainingOf := func(cc string) int64 {
		idx := strings.Index(cc, "max-age=")
		if idx < 0 {
			return -1
		}
		v, _ := strconv.ParseInt(cc[idx+len("max-age="):], 10, 64)
		return v
	}

	// Fresh URL: max-age close to TTL (604800 minus elapsed seconds).
	signed, _ := signer.Sign("users/1/secret.jpg")
	w := doAssetGet(r, "/api/v1/assets/"+signed)
	cc := w.Header().Get("Cache-Control")
	if !strings.HasPrefix(cc, "private,") {
		t.Errorf("protected assets must use private caching, got %q", cc)
	}
	maxAge := remainingOf(cc)
	if maxAge <= 0 || maxAge > int64((24*7*time.Hour)/time.Second) {
		t.Errorf("max-age %d outside credential lifetime bounds", maxAge)
	}
	if expires := w.Header().Get("Expires"); expires == "" {
		t.Error("Expires header missing")
	}

	// Near-expiry URL: max-age must shrink to the remaining lifetime, not
	// stay at the fixed window.
	nearExpiry := time.Now().Add(90 * time.Second).Unix()
	qNear := url.Values{
		"expires": []string{strconv.FormatInt(nearExpiry, 10)},
		"sig":     []string{signer.sign("users/1/secret.jpg", nearExpiry)},
	}
	w = doAssetGet(r, "/api/v1/assets/users/1/secret.jpg?"+qNear.Encode())
	maxAge = remainingOf(w.Header().Get("Cache-Control"))
	if maxAge <= 0 || maxAge > 91 {
		t.Errorf("near-expiry URL cached too long: max-age=%d", maxAge)
	}

	// Public assets keep intentional long-lived public caching.
	w = doAssetGet(r, "/api/v1/assets/profiles/7/avatar.jpg")
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "public") {
		t.Errorf("public asset should retain shared caching, got %q", cc)
	}

	// Rejected requests must not be stored by caches.
	w = doAssetGet(r, "/api/v1/assets/users/1/secret.jpg")
	if cc := w.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("rejected request must send no-store, got %q", cc)
	}
}

// TestLegacyPrefixStripIsAnchored pins the anchored legacy normalization:
// only the exact "uploads/" prefix is stripped, so a protected path that
// merely CONTAINS "profiles/" can never be re-rooted into the public
// namespace.
func TestLegacyPrefixStripIsAnchored(t *testing.T) {
	cfg := mediaTestConfig("jwt-a", "anchored-strip-media-key-012345", false)
	r, _, _ := newAssetRouter(t, cfg)

	// Legacy layout (uploads/profiles/...) remains publicly reachable.
	if w := doAssetGet(r, "/api/v1/assets/uploads/profiles/7/avatar.jpg"); w.Code != http.StatusOK {
		t.Fatalf("legacy uploads/profiles public asset broken: %d %s", w.Code, w.Body.String())
	}

	// A protected-looking path that merely contains profiles/ is NOT
	// re-rooted: without a signature it must be rejected, not classified
	// public. users/1/secret.jpg is never reachable this way.
	w := doAssetGet(r, "/api/v1/assets/users/1/profiles/7/avatar.jpg")
	if w.Code == http.StatusOK && w.Body.String() == "PUBLIC" {
		t.Fatal("substring profiles/ re-rooted a non-legacy path into the public namespace")
	}
}
