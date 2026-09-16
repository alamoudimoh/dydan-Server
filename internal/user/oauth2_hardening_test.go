package user

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	auth "github.com/alamoudimoh/dydan-server/internal/auth"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// oauthTestHarness wires a Handler against a local fake OIDC provider and an
// in-memory SQLite database. No external services are involved.
type oauthTestHarness struct {
	handler *Handler
	router  *gin.Engine
	idp     *fakeIdPUserPackage
	db      *gorm.DB
}

type fakeIdPUserPackage struct {
	server       *httptest.Server
	lastVerifier string
	userInfo     map[string]any
}

func newFakeIdP(t *testing.T, userInfo map[string]any) *fakeIdPUserPackage {
	t.Helper()
	f := &fakeIdPUserPackage{userInfo: userInfo}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		f.lastVerifier = r.PostFormValue("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"access_token": "at", "token_type": "Bearer"})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.userInfo)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func TestNativeOAuthRedirectURIsUseDydan(t *testing.T) {
	allowed := (&Handler{}).allowedOAuthRedirectURIs()
	if !allowed["dydan://auth/oauth2"] {
		t.Fatal("canonical Dydan redirect URI is not allowed")
	}
}

func newOAuthHarness(t *testing.T, userInfo map[string]any, allowLegacy bool) *oauthTestHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	f := newFakeIdP(t, userInfo)

	gdb, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := gdb.AutoMigrate(&uModel.User{}, &uModel.UserSession{}, &cModel.Circle{}, &cModel.UserCircle{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := &config.Config{}
	cfg.Jwt.SessionTime = time.Hour
	cfg.Jwt.MaxRefresh = 24 * time.Hour
	cfg.Jwt.Secret = "test-secret-that-is-long-enough-1234"
	cfg.Server.PublicHost = "https://dydan.example.com"
	cfg.OAuth2Config = config.OAuth2Config{
		ClientID:              "test-client",
		ClientSecret:          "test-secret",
		AuthURL:               f.server.URL + "/auth",
		TokenURL:              f.server.URL + "/token",
		UserInfoURL:           f.server.URL + "/userinfo",
		RedirectURL:           "https://configured.example.com/auth/oauth2",
		Scopes:                []string{"openid"},
		AllowLegacyOAuthState: allowLegacy,
	}

	repo := uRepo.NewUserRepository(gdb, cfg)
	jwtAuth, err := auth.NewAuthMiddleware(cfg, repo, nil)
	if err != nil {
		t.Fatalf("jwt middleware: %v", err)
	}

	h := &Handler{
		userRepo:          repo,
		tokenService:      auth.NewTokenService(repo, jwtAuth, cfg),
		identityProvider:  auth.NewIdentityProvider(cfg),
		oauthTransactions: auth.NewOAuthTransactionStore(),
		oauth2Config:      cfg.OAuth2Config,
		oauthRedirectURLs: []string{sameOriginRedirect},
	}

	r := gin.New()
	authRoutes := r.Group("api/v1/auth")
	authRoutes.POST("/:provider/start", h.thirdPartyAuthStart)
	authRoutes.POST("/:provider/callback", h.thirdPartyAuthCallback)

	return &oauthTestHarness{handler: h, router: r, idp: f, db: gdb}
}

func (th *oauthTestHarness) post(t *testing.T, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "dydan.example.com")
	req.RemoteAddr = "192.0.2.1:1234"
	w := httptest.NewRecorder()
	th.router.ServeHTTP(w, req)
	return w
}

func (th *oauthTestHarness) seedUser(t *testing.T, email, username string) {
	t.Helper()
	if err := th.db.Create(&uModel.User{
		Username: username,
		Email:    email,
		Password: "not-a-real-hash",
		CircleID: 1,
	}).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
}

const sameOriginRedirect = "https://dydan.example.com/auth/oauth2"

func TestOAuthStartIssuesStateAndChallenge(t *testing.T) {
	th := newOAuthHarness(t, map[string]any{}, false)

	w := th.post(t, "/api/v1/auth/oauth2/start", map[string]any{})
	if w.Code != http.StatusOK {
		t.Fatalf("start failed: %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		State              string `json:"state"`
		CodeChallenge      string `json:"code_challenge"`
		CodeChallengeMethd string `json:"code_challenge_method"`
	}
	_ = json.Unmarshal(w.Body.Bytes(), &resp)
	if len(resp.State) < 32 {
		t.Fatalf("state too weak: %q", resp.State)
	}
	if resp.CodeChallenge == "" || resp.CodeChallengeMethd != "S256" {
		t.Fatalf("missing PKCE parameters: %+v", resp)
	}
}

func TestOAuthStartUnknownProviderRejected(t *testing.T) {
	th := newOAuthHarness(t, map[string]any{}, false)
	if w := th.post(t, "/api/v1/auth/google/start", map[string]any{}); w.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for unknown provider start, got %d", w.Code)
	}
}

func TestOAuthStartUnapprovedRedirectRejected(t *testing.T) {
	th := newOAuthHarness(t, map[string]any{}, false)
	w := th.post(t, "/api/v1/auth/oauth2/start", map[string]any{
		"redirect_uri": "https://evil.example.com/auth/oauth2",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unapproved redirect, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestOAuthStartAcceptsConfiguredPublicRedirect(t *testing.T) {
	th := newOAuthHarness(t, map[string]any{}, false)
	w := th.post(t, "/api/v1/auth/oauth2/start", map[string]any{
		"redirect_uri": sameOriginRedirect,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("same-origin redirect should be allowed, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestOAuthStartRejectsUnconfiguredForwardedOrigin(t *testing.T) {
	th := newOAuthHarness(t, map[string]any{}, false)
	payload, _ := json.Marshal(map[string]any{"redirect_uri": "https://evil.example.com/auth/oauth2"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/oauth2/start", strings.NewReader(string(payload)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Forwarded-Proto", "https")
	req.Header.Set("X-Forwarded-Host", "dydan.example.com")
	req.RemoteAddr = "203.0.113.10:1234"
	w := httptest.NewRecorder()
	th.router.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("forwarding headers must not authorize an unconfigured redirect, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestOAuthCallbackInvalidStateRejected(t *testing.T) {
	th := newOAuthHarness(t, map[string]any{"sub": "u1", "email": "bob@example.com"}, false)

	w := th.post(t, "/api/v1/auth/oauth2/callback", map[string]any{
		"code":         "auth-code",
		"state":        "forged-or-replayed-state",
		"redirect_uri": sameOriginRedirect,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for unknown state, got %d (%s)", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "state") && !strings.Contains(w.Body.String(), "sign-in") {
		t.Fatalf("unclear error message: %s", w.Body.String())
	}
}

func TestOAuthCallbackMissingStateRejected(t *testing.T) {
	th := newOAuthHarness(t, map[string]any{}, false)
	w := th.post(t, "/api/v1/auth/oauth2/callback", map[string]any{
		"code":         "auth-code",
		"redirect_uri": sameOriginRedirect,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing state, got %d", w.Code)
	}
}

func TestOAuthCallbackExpiredStateRejected(t *testing.T) {
	th := newOAuthHarness(t, map[string]any{}, false)
	// Swap in a store with a tiny TTL.
	th.handler.oauthTransactions = auth.NewOAuthTransactionStoreWithTTL(1 * time.Millisecond)
	txn, err := th.handler.oauthTransactions.Start(sameOriginRedirect)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)

	w := th.post(t, "/api/v1/auth/oauth2/callback", map[string]any{
		"code":         "auth-code",
		"state":        txn.State,
		"redirect_uri": sameOriginRedirect,
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for expired state, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestOAuthCallbackInvalidRedirectRejectedEvenWithValidState(t *testing.T) {
	th := newOAuthHarness(t, map[string]any{}, false)
	txn, err := th.handler.oauthTransactions.Start(sameOriginRedirect)
	if err != nil {
		t.Fatal(err)
	}
	w := th.post(t, "/api/v1/auth/oauth2/callback", map[string]any{
		"code":         "auth-code",
		"state":        txn.State,
		"redirect_uri": "https://evil.example.com/auth/oauth2",
	})
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for attacker redirect, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestOAuthCallbackValidFlowAndReplayProtection(t *testing.T) {
	th := newOAuthHarness(t, map[string]any{
		"sub":            "user-1",
		"email":          "bob@example.com",
		"email_verified": true,
	}, false)
	th.seedUser(t, "bob@example.com", "bob")

	startResp := th.post(t, "/api/v1/auth/oauth2/start", map[string]any{
		"redirect_uri": sameOriginRedirect,
	})
	var start struct {
		State         string `json:"state"`
		CodeChallenge string `json:"code_challenge"`
	}
	_ = json.Unmarshal(startResp.Body.Bytes(), &start)

	w := th.post(t, "/api/v1/auth/oauth2/callback", map[string]any{
		"code":         "one-time-code",
		"state":        start.State,
		"redirect_uri": sameOriginRedirect,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("valid flow failed: %d %s", w.Code, w.Body.String())
	}
	var tokens map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &tokens)
	if tokens["token"] == "" || tokens["refresh_token"] == "" {
		t.Fatalf("expected access+refresh tokens, got %v", tokens)
	}

	// PKCE: verifier delivered to the token endpoint must hash to the
	// challenge issued at start.
	if th.idp.lastVerifier == "" {
		t.Fatal("no code_verifier sent to token endpoint")
	}
	if auth.PKCES256Challenge(th.idp.lastVerifier) != start.CodeChallenge {
		t.Fatal("sent verifier does not match the issued challenge")
	}

	// Replay of the same state must be rejected.
	w2 := th.post(t, "/api/v1/auth/oauth2/callback", map[string]any{
		"code":         "another-one-time-code",
		"state":        start.State,
		"redirect_uri": sameOriginRedirect,
	})
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("replayed state accepted: %d", w2.Code)
	}
}

func TestOAuthCallbackUnverifiedEmailCannotLinkExistingAccount(t *testing.T) {
	for _, claims := range []map[string]any{
		{"sub": "u1", "email": "bob@example.com", "email_verified": false},
		{"sub": "u1", "email": "bob@example.com"}, // claim absent entirely
	} {
		th := newOAuthHarness(t, claims, false)
		th.seedUser(t, "bob@example.com", "bob")

		txn, err := th.handler.oauthTransactions.Start(sameOriginRedirect)
		if err != nil {
			t.Fatal(err)
		}
		w := th.post(t, "/api/v1/auth/oauth2/callback", map[string]any{
			"code":         "auth-code",
			"state":        txn.State,
			"redirect_uri": sameOriginRedirect,
		})
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("claims=%v: expected 401 for unverified email link, got %d (%s)",
				claims, w.Code, w.Body.String())
		}
	}
}

func TestOAuthCallbackExplicitlyUnverifiedEmailAlwaysRejected(t *testing.T) {
	// Even with no existing account, email_verified=false must not create one.
	th := newOAuthHarness(t, map[string]any{
		"sub":            "new-user",
		"email":          "fresh@example.com",
		"email_verified": false,
	}, false)

	txn, err := th.handler.oauthTransactions.Start(sameOriginRedirect)
	if err != nil {
		t.Fatal(err)
	}
	w := th.post(t, "/api/v1/auth/oauth2/callback", map[string]any{
		"code":         "auth-code",
		"state":        txn.State,
		"redirect_uri": sameOriginRedirect,
	})
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 for unverified email, got %d (%s)", w.Code, w.Body.String())
	}
}

func TestOAuthCallbackLegacyFallbackOptIn(t *testing.T) {
	// With AllowLegacyOAuthState enabled (opt-in escape hatch), callbacks from
	// clients that cannot present server state still work when everything else
	// checks out. Default builds reject them (see tests above).
	th := newOAuthHarness(t, map[string]any{
		"sub":            "user-legacy",
		"email":          "legacy@example.com",
		"email_verified": true,
	}, true)
	th.seedUser(t, "legacy@example.com", "legacy")

	w := th.post(t, "/api/v1/auth/oauth2/callback", map[string]any{
		"code":         "auth-code",
		"redirect_uri": sameOriginRedirect,
	})
	if w.Code != http.StatusOK {
		t.Fatalf("legacy fallback should pass with opt-in, got %d (%s)", w.Code, w.Body.String())
	}

	// But even the legacy path rejects unapproved redirects.
	w2 := th.post(t, "/api/v1/auth/oauth2/callback", map[string]any{
		"code":         "auth-code",
		"redirect_uri": "https://evil.example.com/auth/oauth2",
	})
	if w2.Code != http.StatusBadRequest {
		t.Fatalf("legacy path must still validate redirects, got %d", w2.Code)
	}
}
