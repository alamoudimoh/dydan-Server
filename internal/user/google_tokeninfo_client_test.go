package user

import (
	"net/http"
	"testing"
	"time"
)

// Regression (Phase 8 closure audit): the third-party auth callback used
// http.DefaultClient for the server-side Google tokeninfo call. That route is
// unauthenticated and attacker-supplied tokens trigger the outbound request,
// so an unbounded client let stalled upstream connections pin handler
// goroutines indefinitely.
func TestGoogleTokeninfoClientIsBounded(t *testing.T) {
	c := googleTokeninfoClient()
	if c == http.DefaultClient {
		t.Fatal("tokeninfo verification still uses the shared unbounded default client")
	}
	if c.Timeout <= 0 {
		t.Fatalf("tokeninfo client timeout = %v, want bounded", c.Timeout)
	}
	if c.Timeout != 10*time.Second {
		t.Fatalf("timeout = %v, want 10s", c.Timeout)
	}
}
