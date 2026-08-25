// Package outbound provides the shared HTTP client used for every
// attacker-influenced server-side request (circle webhooks, notification
// webhooks, identity-provider calls). It validates the effective connection
// destination — not just the URL string — so hostname tricks, DNS answers,
// and redirects cannot pivot a request into infrastructure the caller must
// not reach.
package outbound

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
)

const maxRedirects = 5

// Resolver resolves hostnames to addresses so destination policy can be
// enforced against the actual IP before any connection is made.
type Resolver interface {
	LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)
}

// ClientOptions configures a hardened client.
//
// AllowPrivateNetworks exists for self-hosted deployments whose webhook or
// API consumers legitimately live on the LAN (Home Assistant, MinIO-style
// integrations, internal alerting). It is an explicit operator opt-in, never
// a default: on hosted/shared deployments an authenticated actor must not be
// able to make the server speak to loopback, RFC1918, link-local (including
// cloud metadata) or unspecified addresses.
type ClientOptions struct {
	Timeout              time.Duration
	Resolver             Resolver
	AllowPrivateNetworks bool
	RequireHTTPS         bool
	AllowedHosts         []string
}

type Client struct {
	http  *http.Client
	opts  ClientOptions
	dialr *net.Dialer
}

// NewClient builds a hardened client. A nil resolver falls back to net.DefaultResolver.
func NewClient(opts ClientOptions) *Client {
	if opts.Timeout <= 0 {
		opts.Timeout = 10 * time.Second
	}
	c := &Client{
		opts:  opts,
		dialr: &net.Dialer{Timeout: 10 * time.Second},
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DialContext = c.dialContext
	transport.TLSHandshakeTimeout = 10 * time.Second
	transport.ResponseHeaderTimeout = opts.Timeout
	transport.ExpectContinueTimeout = time.Second
	c.http = &http.Client{
		Timeout:       opts.Timeout,
		Transport:     transport,
		CheckRedirect: c.checkRedirect,
	}
	return c
}

// Std exposes the underlying *http.Client for callers that need
// NewRequestWithContext + Do semantics. The redirect policy and destination
// dialer still apply to every request issued through it.
func (c *Client) Std() *http.Client { return c.http }

// ValidateURLSyntax enforces the structural rules every externally
// influenced destination must satisfy before it is persisted or dialed:
// absolute http(s), no embedded credentials, no fragment. It does not judge
// the host's network location — that happens at dial time.
func ValidateURLSyntax(raw string) error {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return fmt.Errorf("URL has no host")
	}
	if u.User != nil {
		return fmt.Errorf("URL credentials are not allowed")
	}
	if u.Fragment != "" {
		return fmt.Errorf("URL fragment is not allowed")
	}
	return nil
}

func isPublicAddress(addr netip.Addr) bool {
	a := addr.Unmap()
	if !a.IsValid() {
		return false
	}
	if a.IsLoopback() || a.IsPrivate() || a.IsLinkLocalUnicast() || a.IsLinkLocalMulticast() ||
		a.IsUnspecified() || a.IsMulticast() || a.IsInterfaceLocalMulticast() {
		return false
	}
	return true
}

func (c *Client) validateHost(host string) error {
	host = strings.ToLower(strings.TrimSuffix(host, "."))
	if host == "" {
		return fmt.Errorf("empty host")
	}
	if len(c.opts.AllowedHosts) > 0 {
		for _, allowed := range c.opts.AllowedHosts {
			if strings.ToLower(allowed) == host {
				return nil
			}
		}
		return fmt.Errorf("host %q is not allowed", host)
	}
	return nil
}

func (c *Client) validateAddr(addr netip.Addr) error {
	if c.opts.AllowPrivateNetworks {
		return nil
	}
	if !isPublicAddress(addr) {
		return fmt.Errorf("refusing to connect to non-public address %q", addr)
	}
	return nil
}

// validateDestination applies URL-level policy; network-level checks happen
// in dialContext so they bind to the resolved address actually dialed.
func (c *Client) validateDestination(u *url.URL) error {
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported scheme %q", u.Scheme)
	}
	if c.opts.RequireHTTPS && u.Scheme != "https" {
		return fmt.Errorf("destination scheme %q is not allowed; https required", u.Scheme)
	}
	if err := c.validateHost(u.Hostname()); err != nil {
		return err
	}
	if ip, err := netip.ParseAddr(strings.TrimSuffix(u.Hostname(), ".")); err == nil {
		if err := c.validateAddr(ip); err != nil {
			return err
		}
	}
	return nil
}

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("stopped after %d redirects", maxRedirects)
	}
	first := via[0]
	next := req.URL
	if next.Host != first.URL.Host {
		return fmt.Errorf("cross-host redirect from %q to %q is not allowed", first.URL.Host, next.Host)
	}
	return c.validateDestination(next)
}

// dialContext resolves the destination, refuses non-public addresses unless
// explicitly opted in, and dials the validated IP directly. Dialing the
// validated address instead of handing back the hostname removes the
// validate-then-re-resolve window (DNS rebinding / multiple-answer fallback).
func (c *Client) dialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("invalid dial address %q: %w", addr, err)
	}
	if err := c.validateHost(host); err != nil {
		return nil, err
	}
	resolver := c.opts.Resolver
	if resolver == nil {
		resolver = net.DefaultResolver
	}
	addrs, err := resolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve %q: %w", host, err)
	}
	if len(addrs) == 0 {
		return nil, fmt.Errorf("no addresses for %q", host)
	}
	// Fail closed if ANY answer is non-public: a mixed honest/private answer
	// set is exactly how rebinding slips past first-address-only checks.
	for _, a := range addrs {
		if err := c.validateAddr(a); err != nil {
			return nil, err
		}
	}
	chosen := addrs[0].Unmap().String()
	return c.dialr.DialContext(ctx, network, net.JoinHostPort(chosen, port))
}

// Do issues req under the shared destination policy.
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	if req.URL != nil {
		if err := c.validateDestination(req.URL); err != nil {
			return nil, err
		}
	}
	return c.http.Do(req)
}

// Get issues a GET under the shared destination policy.
func (c *Client) Get(url string) (*http.Response, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return c.Do(req)
}
