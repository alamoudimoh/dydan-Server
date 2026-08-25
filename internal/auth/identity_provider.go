package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	"golang.org/x/oauth2"
)

type IdentityProviderUserInfo struct {
	Identifier  string
	Username    string
	DisplayName string
	Email       string
	Picture     string
	Groups      []string
	// EmailVerified mirrors the OIDC email_verified claim:
	//   nil            -> provider did not expose the claim
	//   true/false     -> provider's explicit assertion
	EmailVerified *bool
}

type IdentityProvider struct {
	config    *config.OAuth2Config
	isEnabled bool
	// httpClient bounds every outbound call to the identity provider so a hung
	// upstream cannot stall login requests or the notification scheduler.
	httpClient *http.Client
}

// idpHTTPTimeout bounds every outbound call to the identity provider so a
// hung upstream cannot stall login requests or the notification scheduler.
const idpHTTPTimeout = 10 * time.Second

// maxUserInfoResponseBytes caps how much of a userinfo response is buffered
// before parsing. Legitimate IdP userinfo payloads are tiny; anything larger
// is treated as hostile/misconfigured rather than read into memory.
const maxUserInfoResponseBytes = 1 << 20 // 1 MiB

func NewIdentityProvider(cfg *config.Config) *IdentityProvider {
	if cfg.OAuth2Config.ClientID == "" || cfg.OAuth2Config.ClientSecret == "" {
		return &IdentityProvider{isEnabled: false, httpClient: &http.Client{Timeout: idpHTTPTimeout}}
	}
	return &IdentityProvider{config: &cfg.OAuth2Config, isEnabled: true, httpClient: &http.Client{Timeout: idpHTTPTimeout}}
}

func (i *IdentityProvider) Enabled() bool {
	return i.isEnabled
}

// oauth2ConfigFor builds the provider config for a specific redirect URI.
func (i *IdentityProvider) oauth2ConfigFor(redirectURI string) *oauth2.Config {
	return &oauth2.Config{
		ClientID:     i.config.ClientID,
		ClientSecret: i.config.ClientSecret,
		RedirectURL:  redirectURI,
		Scopes:       i.config.Scopes,
		Endpoint: oauth2.Endpoint{
			AuthURL:  i.config.AuthURL,
			TokenURL: i.config.TokenURL,
		},
	}
}

// AuthCodeURL builds the provider authorization URL for a pending transaction.
// When codeChallenge is non-empty the flow uses PKCE (S256).
func (i *IdentityProvider) AuthCodeURL(redirectURI, state, codeChallenge string) string {
	opts := []oauth2.AuthCodeOption{}
	if codeChallenge != "" {
		opts = append(opts,
			oauth2.SetAuthURLParam("code_challenge", codeChallenge),
			oauth2.SetAuthURLParam("code_challenge_method", "S256"),
		)
	}
	return i.oauth2ConfigFor(redirectURI).AuthCodeURL(state, opts...)
}

func (i *IdentityProvider) ExchangeToken(ctx context.Context, code string, redirectURL string, codeVerifier ...string) (string, error) {
	if !i.isEnabled {
		return "", errors.New("identity provider is not enabled")
	}

	if code == "" {
		return "", errors.New("authorization code is empty")
	}

	// Use provided redirect URL if given, otherwise fall back to config.
	redirect := i.config.RedirectURL
	if len(redirectURL) > 0 && redirectURL != "" {
		redirect = redirectURL
	}

	conf := i.oauth2ConfigFor(redirect)

	opts := []oauth2.AuthCodeOption{}
	for _, verifier := range codeVerifier {
		if verifier != "" {
			opts = append(opts, oauth2.SetAuthURLParam("code_verifier", verifier))
		}
	}

	// Bind the exchange to the shared bounded client: x/oauth2 otherwise
	// falls back to http.DefaultClient (no timeout), letting a stalled token
	// endpoint pin login handlers indefinitely.
	ctx = context.WithValue(ctx, oauth2.HTTPClient, i.httpClient)
	token, err := conf.Exchange(ctx, code, opts...)
	if err != nil {
		// Enhanced error handling for OAuth2 errors
		switch {
		case strings.Contains(err.Error(), "invalid_grant"):
			return "", errors.New("oauth2: invalid_grant - The authorization code is invalid, expired, revoked, or does not match the redirect URI")
		case strings.Contains(err.Error(), "invalid_client"):
			return "", errors.New("oauth2: invalid_client - Client authentication failed")
		case strings.Contains(err.Error(), "invalid_request"):
			return "", errors.New("oauth2: invalid_request - The request is missing a required parameter or is otherwise malformed")
		default:
			return "", err
		}
	}

	accessToken, ok := token.AccessToken, token.Valid()
	if !ok {
		return "", errors.New("access token not found or invalid")
	}

	return accessToken, nil
}

func (i *IdentityProvider) GetUserInfo(ctx context.Context, accessToken string) (*IdentityProviderUserInfo, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", i.config.UserInfoURL, nil)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/json")

	resp, err := i.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("userinfo endpoint returned status %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUserInfoResponseBytes+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxUserInfoResponseBytes {
		return nil, errors.New("userinfo response too large")
	}

	var claims map[string]any
	err = json.Unmarshal(body, &claims)
	if err != nil {
		return nil, errors.New("failed to unmarshal claims")
	}
	userInfo := IdentityProviderUserInfo{}
	// Use comma-ok assertions: a compliant-but-unusual IdP returning non-string
	// claim values must not panic the handler.
	if val, ok := claims["sub"]; ok {
		userInfo.Identifier, _ = val.(string)
	}
	if val, ok := claims["preferred_username"]; ok {
		userInfo.Username, _ = val.(string)
	}
	if val, ok := claims["name"]; ok {
		userInfo.DisplayName, _ = val.(string)
	}
	if val, ok := claims["email"]; ok {
		userInfo.Email, _ = val.(string)
	}
	// email_verified may arrive as a JSON bool or, from non-compliant IdPs,
	// as the strings "true"/"false". Absent -> nil (unknown).
	if raw, present := claims["email_verified"]; present {
		switch v := raw.(type) {
		case bool:
			userInfo.EmailVerified = &v
		case string:
			parsed := strings.EqualFold(v, "true")
			userInfo.EmailVerified = &parsed
		}
	}
	if val, ok := claims["picture"]; ok {
		pictureURL, _ := val.(string)
		if isFetchableImageReference(pictureURL) {
			userInfo.Picture = pictureURL
		}
	}
	if val, ok := claims["groups"]; ok {
		if groups, ok := val.([]interface{}); ok {
			for _, g := range groups {
				if s, ok := g.(string); ok {
					userInfo.Groups = append(userInfo.Groups, s)
				}
			}
		}
	}
	return &userInfo, nil
}

// isFetchableImageReference accepts only absolute HTTP(S) URLs without
// embedded credentials. DayDan deliberately does NOT probe picture URLs
// server-side: the claim value is attacker- or IdP-controlled input, and a
// reachability GET with default redirects was an SSRF primitive against
// internal infrastructure. Clients render the image from their own network.
func isFetchableImageReference(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}
	return u.Scheme == "http" || u.Scheme == "https"
}
