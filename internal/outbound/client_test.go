package outbound

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type staticResolver map[string][]netip.Addr

func (r staticResolver) LookupNetIP(_ context.Context, _, host string) ([]netip.Addr, error) {
	addrs, ok := r[strings.ToLower(strings.TrimSuffix(host, "."))]
	if !ok {
		return nil, errors.New("host not found")
	}
	return addrs, nil
}

func TestValidateURLSyntax(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		ok   bool
	}{
		{"HTTPS", "https://example.com:8443/hook?x=1", true},
		{"HTTP", "http://example.com/hook", true},
		{"mixed case and trailing dot", "HTTPS://Example.COM./hook", true},
		{"missing host", "https:///hook", false},
		{"userinfo", "https://user:secret@example.com/hook", false},
		{"fragment", "https://example.com/hook#secret", false},
		{"file", "file:///etc/passwd", false},
		{"gopher", "gopher://example.com/", false},
		{"scheme smuggling", "https%3A%2F%2Fexample.com", false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateURLSyntax(tc.raw); (err == nil) != tc.ok {
				t.Fatalf("ValidateURLSyntax(%q) error = %v, want ok=%v", tc.raw, err, tc.ok)
			}
		})
	}
}

func TestPublicClientRejectsNonPublicDestinations(t *testing.T) {
	resolver := staticResolver{
		"loopback.test": {netip.MustParseAddr("127.0.0.1")},
		"private.test":  {netip.MustParseAddr("10.0.0.8")},
		"linklocal.test": {netip.MustParseAddr("169.254.169.254")},
		"ipv6.test":     {netip.MustParseAddr("::1")},
		"mapped.test":   {netip.MustParseAddr("::ffff:127.0.0.1")},
		"zero.test":     {netip.MustParseAddr("0.0.0.0")},
	}
	client := NewClient(ClientOptions{Timeout: time.Second, Resolver: resolver})

	for _, host := range []string{"loopback.test", "private.test", "linklocal.test", "ipv6.test", "mapped.test", "zero.test"} {
		t.Run(host, func(t *testing.T) {
			req, err := http.NewRequest(http.MethodGet, "https://"+host+"/", nil)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := client.Do(req); err == nil || !strings.Contains(err.Error(), "non-public") {
				t.Fatalf("request error = %v, want non-public rejection", err)
			}
		})
	}
}

func TestPublicClientRejectsMixedDNSAnswersBeforeDial(t *testing.T) {
	resolver := staticResolver{
		"mixed.test": {
			netip.MustParseAddr("93.184.216.34"),
			netip.MustParseAddr("127.0.0.1"),
		},
	}
	client := NewClient(ClientOptions{Timeout: time.Second, Resolver: resolver})
	req, _ := http.NewRequest(http.MethodGet, "https://mixed.test/", nil)
	if _, err := client.Do(req); err == nil || !strings.Contains(err.Error(), "non-public") {
		t.Fatalf("request error = %v, want mixed-answer rejection", err)
	}
}

func TestClientRedirectPolicy(t *testing.T) {
	private := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		t.Fatal("private redirect target was reached")
	}))
	defer private.Close()

	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Redirect(w, &http.Request{}, private.URL, http.StatusFound)
	}))
	defer redirect.Close()

	client := NewClient(ClientOptions{Timeout: time.Second, AllowPrivateNetworks: true})
	req, _ := http.NewRequest(http.MethodGet, redirect.URL, nil)
	_, err := client.Do(req)
	if err == nil || !strings.Contains(err.Error(), "cross-host redirect") {
		t.Fatalf("redirect error = %v, want cross-host rejection", err)
	}
}

func TestClientStopsRedirectLoops(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/again", http.StatusFound)
	}))
	defer server.Close()

	client := NewClient(ClientOptions{Timeout: time.Second, AllowPrivateNetworks: true})
	resp, err := client.Get(server.URL)
	if resp != nil {
		resp.Body.Close()
	}
	if err == nil || !strings.Contains(err.Error(), "redirects") {
		t.Fatalf("redirect error = %v, want redirect limit", err)
	}
}

func TestAllowedHosts(t *testing.T) {
	client := NewClient(ClientOptions{
		Timeout:              time.Second,
		AllowPrivateNetworks: true,
		AllowedHosts:         []string{"discord.com", "discordapp.com"},
	})
	for _, raw := range []string{
		"https://evil.example/api/webhooks/1/x",
		"https://discord.com.evil.example/api/webhooks/1/x",
		"https://discord.com@evil.example/api/webhooks/1/x",
	} {
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		if _, err := client.Do(req); err == nil || !strings.Contains(err.Error(), "is not allowed") {
			t.Fatalf("%s error = %v, want host rejection", raw, err)
		}
	}
}

func TestPrivateNetworkCompatibilityOptIn(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClient(ClientOptions{Timeout: time.Second, AllowPrivateNetworks: true})
	resp, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("private-network opt-in request failed: %v", err)
	}
	resp.Body.Close()
}

func TestPublicAddressClassification(t *testing.T) {
	for _, raw := range []string{"127.0.0.1", "::1", "10.0.0.1", "172.16.0.1", "192.168.0.1", "169.254.1.1", "fe80::1", "::ffff:127.0.0.1", "0.0.0.0"} {
		if isPublicAddress(netip.MustParseAddr(raw)) {
			t.Errorf("%s classified public", raw)
		}
	}
	if !isPublicAddress(netip.MustParseAddr("93.184.216.34")) {
		t.Error("public IPv4 classified non-public")
	}
}

// TestAdversarialDestinationVariants covers alternate encodings an attacker
// might use to disguise a private destination: numeric IPv4 notation that a
// nonstandard resolver could answer with loopback, trailing-dot hosts,
// zoned link-local addresses, and URL structure tricks (fragments/userinfo)
// that must be rejected before persistence.
func TestAdversarialDestinationVariants(t *testing.T) {
	resolver := staticResolver{
		// Hypothetical resolver that answers numeric hosts like a hostile
		// search-domain environment could: the dial-time check must still
		// refuse the private answer.
		"2130706433":    {netip.MustParseAddr("127.0.0.1")},
		"loopback.test": {netip.MustParseAddr("127.0.0.1")},
	}
	client := NewClient(ClientOptions{Timeout: time.Second, Resolver: resolver})
	for _, raw := range []string{
		"http://2130706433/",
		"http://loopback.test./", // trailing dot
	} {
		req, _ := http.NewRequest(http.MethodGet, raw, nil)
		if _, err := client.Do(req); err == nil || !strings.Contains(err.Error(), "non-public") {
			t.Errorf("%s: error = %v, want non-public rejection", raw, err)
		}
	}
	if isPublicAddress(netip.MustParseAddr("fe80::1%eth0")) {
		t.Error("zoned link-local classified public")
	}
	for _, raw := range []string{
		"https://example.com#@127.0.0.1",
		"https://user@example.com/",
		"https://user:pass@example.com/hook",
	} {
		if err := ValidateURLSyntax(raw); err == nil {
			t.Errorf("%s: accepted, want rejection", raw)
		}
	}
}

func TestClientHonorsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	client := NewClient(ClientOptions{Timeout: time.Minute, AllowPrivateNetworks: true})
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
	if _, err := client.Do(req); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("request error = %v, want deadline exceeded", err)
	}
}

var _ Resolver = staticResolver{}
