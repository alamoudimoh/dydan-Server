package auth

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/golang-jwt/jwt/v4"
	"gorm.io/gorm"
)

// newTransportHarness builds a router exposing the API-token middleware and
// the multi-auth middleware over an isolated SQLite database, so the exact
// HTTP contract of credential transport can be asserted end to end.
func newTransportHarness(t *testing.T) (*gin.Engine, *uRepo.UserRepository, *uModel.User) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(
		filepath.ToSlash(filepath.Join(t.TempDir(), "transport.db"))+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.APIToken{}, &cModel.Circle{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{}
	cfg.Jwt.Secret = "api-token-transport-test-secret-123456"
	cfg.Jwt.SessionTime = time.Hour
	repo := uRepo.NewUserRepository(db, cfg)
	owner := &uModel.User{Username: "transport-owner", Email: "transport@example.com"}
	if _, err := repo.CreateUser(context.Background(), owner); err != nil {
		t.Fatal(err)
	}
	jwtAuth, err := NewAuthMiddleware(cfg, repo, nil)
	if err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	apiOnly := r.Group("/api")
	apiOnly.Use(APITokenMiddleware(repo))
	apiOnly.GET("/token-only", func(c *gin.Context) {
		user, _ := c.Get(identityKey)
		details := user.(*uModel.UserDetails)
		c.JSON(http.StatusOK, gin.H{"user_id": details.ID})
	})

	multi := NewMultiAuthMiddleware(jwtAuth, repo)
	multiRoutes := r.Group("/multi")
	multiRoutes.Use(multi.MiddlewareFunc())
	multiRoutes.GET("/either", func(c *gin.Context) {
		user, _ := c.Get(identityKey)
		details := user.(*uModel.UserDetails)
		c.JSON(http.StatusOK, gin.H{"user_id": details.ID})
	})
	return r, repo, owner
}

func doAPI(r *gin.Engine, method, target string, headers map[string][]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	for k, vs := range headers {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

const (
	validToken   = "aaaa0000bbbb1111cccc2222dddd3333eeee4444ffff55556666777788889999"
	unknownToken = "1111000011110000111100001111000011110000111100001111000011110000"
)

func seedTransportTokens(t *testing.T, r *gin.Engine, repo *uRepo.UserRepository, owner *uModel.User) {
	t.Helper()
	if _, err := repo.StoreAPIToken(context.Background(), owner.ID, "primary", validToken); err != nil {
		t.Fatal(err)
	}
}

// TestAPITokenHeaderContract pins the deterministic secretkey header contract:
// only well-formed credentials authenticate, the stored digest can never be
// replayed as a credential, duplicate header values resolve deterministically
// (first value wins), and no error response ever reflects the credential.
func TestAPITokenHeaderContract(t *testing.T) {
	r, repo, owner := newTransportHarness(t)
	seedTransportTokens(t, r, repo, owner)
	digest := uRepo.APITokenSecretHash(validToken)

	cases := []struct {
		name    string
		headers map[string][]string
		want    int
	}{
		{"valid token", map[string][]string{"secretkey": {validToken}}, http.StatusOK},
		{"empty credential", map[string][]string{"secretkey": {""}}, http.StatusUnauthorized},
		{"missing credential", nil, http.StatusUnauthorized},
		{"malformed short", map[string][]string{"secretkey": {"abc"}}, http.StatusUnauthorized},
		{"malformed non-hex", map[string][]string{"secretkey": {strings.Repeat("z", 64)}}, http.StatusUnauthorized},
		{"stored-format replay rejected", map[string][]string{"secretkey": {digest}}, http.StatusUnauthorized},
		{"prefixed garbage rejected", map[string][]string{"secretkey": {"sha256:not-a-real-digest-value"}}, http.StatusUnauthorized},
		{"unknown token", map[string][]string{"secretkey": {unknownToken}}, http.StatusUnauthorized},
		{"duplicate identical values accepted deterministically",
			map[string][]string{"secretkey": {validToken, validToken}}, http.StatusOK},
		{"duplicate conflicting values resolve to first", map[string][]string{"secretkey": {validToken, unknownToken}}, http.StatusOK},
		{"duplicate conflicting values: second value never rescues an invalid first",
			map[string][]string{"secretkey": {unknownToken, validToken}}, http.StatusUnauthorized},
		// Whitespace handling: RFC 9110 OWS around field values is stripped
		// by Go's protocol parser before any handler runs (universal,
		// deterministic transport behavior); what this asserts is that the
		// APPLICATION never trims or normalizes credentials on its own — a
		// padded value that reaches the middleware intact is simply rejected,
		// so there is no alternate authentication path via padding tricks.
		{"application does not trim padded credentials",
			map[string][]string{"secretkey": {" " + validToken}}, http.StatusUnauthorized},
		{"case change alters credential", map[string][]string{"secretkey": {strings.ToUpper(validToken[:8]) + validToken[8:]}}, http.StatusUnauthorized},
		{"credential in query string is ignored",
			map[string][]string{}, http.StatusUnauthorized},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			target := "/api/token-only"
			if tc.name == "credential in query string is ignored" {
				target += "?secretkey=" + validToken
			}
			w := doAPI(r, http.MethodGet, target, tc.headers)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d (body: %s)", w.Code, tc.want, w.Body.String())
			}
			for _, secret := range []string{validToken, unknownToken, digest} {
				if strings.Contains(w.Body.String(), secret) {
					t.Fatalf("response reflected credential material: %s", w.Body.String())
				}
			}
		})
	}

	// Revocation: deleting the token ends its access immediately.
	rows, err := repo.GetAllUserTokens(context.Background(), owner.ID)
	if err != nil || len(rows) == 0 {
		t.Fatalf("list tokens: %v", err)
	}
	if err := repo.DeleteAPIToken(context.Background(), owner.ID, fmt.Sprint(rows[0].ID)); err != nil {
		t.Fatal(err)
	}
	if w := doAPI(r, http.MethodGet, "/api/token-only", map[string][]string{"secretkey": {validToken}}); w.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token must not authenticate, got %d", w.Code)
	}
}

// TestMultiAuthPrecedenceIsDeterministic pins authentication precedence: a
// non-empty secretkey header commits the request to API-token authentication
// (invalid token => 401, never JWT fallback), while no header falls through
// to JWT. A JWT-looking value in secretkey must never authenticate.
func TestMultiAuthPrecedenceIsDeterministic(t *testing.T) {
	r, repo, owner := newTransportHarness(t)
	seedTransportTokens(t, r, repo, owner)

	// Valid API key wins.
	if w := doAPI(r, http.MethodGet, "/multi/either", map[string][]string{"secretkey": {validToken}}); w.Code != http.StatusOK {
		t.Fatalf("valid api key must pass multi-auth, got %d", w.Code)
	}

	// Invalid API key: 401 without falling through to any other scheme.
	if w := doAPI(r, http.MethodGet, "/multi/either", map[string][]string{"secretkey": {unknownToken}}); w.Code != http.StatusUnauthorized {
		t.Fatalf("invalid api key must fail closed, got %d", w.Code)
	}

	// An access-JWT-shaped credential presented via secretkey is not a valid
	// API token and must not authenticate (no cross-scheme confusion).
	fakeJWT := makeFakeHS256(t)
	if w := doAPI(r, http.MethodGet, "/multi/either", map[string][]string{"secretkey": {fakeJWT}}); w.Code != http.StatusUnauthorized {
		t.Fatalf("jwt-shaped secretkey must not authenticate as API token, got %d", w.Code)
	}

	// Authorization header alone cannot reach API-token middleware routes.
	if w := doAPI(r, http.MethodGet, "/api/token-only", map[string][]string{"Authorization": {"Bearer " + validToken}}); w.Code != http.StatusUnauthorized {
		t.Fatalf("authorization bearer must not substitute for secretkey, got %d", w.Code)
	}
	_ = jwt.Token{}
}

func makeFakeHS256(t *testing.T) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{"id": 1, "exp": 4102444800})
	signed, err := token.SignedString([]byte("irrelevant-signing-key"))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}
