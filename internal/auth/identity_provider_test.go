package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
)

// newTestIdP spins up a local fake OIDC provider with deterministic
// behaviour. No external service is involved.
type fakeIdP struct {
	server       *httptest.Server
	lastVerifier string
	userInfo     map[string]any
}

func newFakeIdP(t *testing.T, userInfo map[string]any) *fakeIdP {
	t.Helper()
	f := &fakeIdP{userInfo: userInfo}
	mux := http.NewServeMux()
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("token form parse: %v", err)
		}
		f.lastVerifier = r.FormValue("code_verifier")
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"access_token": "test-access-token",
			"token_type":   "Bearer",
		})
	})
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.userInfo)
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func testOAuth2Config(f *fakeIdP) config.OAuth2Config {
	return config.OAuth2Config{
		ClientID:     "test-client",
		ClientSecret: "test-secret",
		AuthURL:      f.server.URL + "/auth",
		TokenURL:     f.server.URL + "/token",
		UserInfoURL:  f.server.URL + "/userinfo",
		Scopes:       []string{"openid", "profile", "email"},
	}
}

func TestGetUserInfoEmailVerifiedVariants(t *testing.T) {
	cases := []struct {
		name   string
		claims map[string]any
		want   *bool
	}{
		{"explicit true", map[string]any{"email_verified": true}, boolPtr(true)},
		{"explicit false", map[string]any{"email_verified": false}, boolPtr(false)},
		{"string true", map[string]any{"email_verified": "true"}, boolPtr(true)},
		{"absent", map[string]any{"sub": "u1"}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeIdP(t, tc.claims)
			idp := NewIdentityProvider(&config.Config{OAuth2Config: testOAuth2Config(f)})
			info, err := idp.GetUserInfo(context.Background(), "tok")
			if err != nil {
				t.Fatalf("GetUserInfo failed: %v", err)
			}
			switch {
			case tc.want == nil && info.EmailVerified != nil:
				t.Fatalf("expected nil EmailVerified, got %v", *info.EmailVerified)
			case tc.want != nil && (info.EmailVerified == nil || *info.EmailVerified != *tc.want):
				t.Fatalf("expected %v, got %v", tc.want, info.EmailVerified)
			}
		})
	}
}

func TestExchangeTokenSendsPKCEVerifier(t *testing.T) {
	f := newFakeIdP(t, map[string]any{"sub": "u1"})
	idp := NewIdentityProvider(&config.Config{OAuth2Config: testOAuth2Config(f)})

	if _, err := idp.ExchangeToken(context.Background(), "code-123", "https://app.example/auth/oauth2", "my-verifier"); err != nil {
		t.Fatalf("ExchangeToken failed: %v", err)
	}
	if f.lastVerifier != "my-verifier" {
		t.Fatalf("token endpoint did not receive code_verifier, got %q", f.lastVerifier)
	}

	// Without a verifier nothing is sent.
	f.lastVerifier = ""
	if _, err := idp.ExchangeToken(context.Background(), "code-123", ""); err != nil {
		t.Fatalf("ExchangeToken (legacy) failed: %v", err)
	}
	if f.lastVerifier != "" {
		t.Fatalf("unexpected code_verifier sent: %q", f.lastVerifier)
	}
}

func TestAuthCodeURLIncludesS256Challenge(t *testing.T) {
	f := newFakeIdP(t, map[string]any{})
	idp := NewIdentityProvider(&config.Config{OAuth2Config: testOAuth2Config(f)})

	raw := idp.AuthCodeURL("https://app.example/auth/oauth2", "state-abc", "challenge-xyz")
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("auth URL invalid: %v", err)
	}
	q := u.Query()
	if q.Get("code_challenge") != "challenge-xyz" {
		t.Fatalf("missing code_challenge in %s", raw)
	}
	if q.Get("code_challenge_method") != "S256" {
		t.Fatalf("wrong code_challenge_method in %s", raw)
	}
	if q.Get("state") != "state-abc" {
		t.Fatalf("missing state in %s", raw)
	}
}

func boolPtr(b bool) *bool { return &b }

func TestDisabledWithoutCredentials(t *testing.T) {
	idp := NewIdentityProvider(&config.Config{})
	if idp.Enabled() {
		t.Fatal("identity provider must be disabled without client credentials")
	}
}

func TestGetUserInfoRejectsErrorStatusAndOversizedBody(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		wantErr string
	}{
		{"error status", http.StatusInternalServerError, `{"sub":"accepted-on-error"}`, "status 500"},
		{"oversized", http.StatusOK, `{"name":"` + strings.Repeat("x", maxUserInfoResponseBytes) + `"}`, "too large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.body)
			}))
			defer server.Close()
			cfg := config.Config{OAuth2Config: config.OAuth2Config{
				ClientID: "id", ClientSecret: "secret", UserInfoURL: server.URL,
			}}
			_, err := NewIdentityProvider(&cfg).GetUserInfo(context.Background(), "token")
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("GetUserInfo error = %v, want %q", err, tc.wantErr)
			}
		})
	}
}

func TestGetUserInfoUsesCallerContext(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()
	cfg := config.Config{OAuth2Config: config.OAuth2Config{
		ClientID: "id", ClientSecret: "secret", UserInfoURL: server.URL,
	}}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := NewIdentityProvider(&cfg).GetUserInfo(ctx, "token")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("GetUserInfo error = %v, want deadline exceeded", err)
	}
}

func TestPictureClaimDoesNotTriggerServerRequest(t *testing.T) {
	var pictureCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("/userinfo", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"picture": "http://127.0.0.1:1/private"})
	})
	mux.HandleFunc("/private", func(http.ResponseWriter, *http.Request) { pictureCalls++ })
	server := httptest.NewServer(mux)
	defer server.Close()
	cfg := config.Config{OAuth2Config: config.OAuth2Config{
		ClientID: "id", ClientSecret: "secret", UserInfoURL: server.URL + "/userinfo",
	}}
	info, err := NewIdentityProvider(&cfg).GetUserInfo(context.Background(), "token")
	if err != nil {
		t.Fatal(err)
	}
	if pictureCalls != 0 {
		t.Fatalf("picture endpoint received %d server requests", pictureCalls)
	}
	if info.Picture != "http://127.0.0.1:1/private" {
		t.Fatalf("valid HTTP picture reference was not preserved: %q", info.Picture)
	}
}

func TestExchangeTokenHasOutboundDeadline(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-release:
		}
	}))
	defer server.Close()
	defer close(release)
	cfg := config.Config{OAuth2Config: config.OAuth2Config{
		ClientID: "id", ClientSecret: "secret", TokenURL: server.URL,
	}}
	idp := NewIdentityProvider(&cfg)
	idp.httpClient.Timeout = 20 * time.Millisecond
	start := time.Now()
	_, err := idp.ExchangeToken(context.Background(), "code", "")
	if err == nil || time.Since(start) > time.Second {
		t.Fatalf("ExchangeToken error = %v after %s (want bounded client deadline)", err, time.Since(start))
	}
}
