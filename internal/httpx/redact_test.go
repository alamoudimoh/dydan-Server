package httpx

import (
	"net/url"
	"strings"
	"testing"
)

const (
	redactTicket = "0123456789abcdef0123456789abcdef"
	redactCode   = "reset-code-9876543210"
)

func TestRedactQueryRedactsSensitiveValuesKeepsNames(t *testing.T) {
	got := RedactQuery("circleId=7&ticket=" + redactTicket + "&includeArchived=true")
	want := "circleId=7&ticket=%5Bredacted%5D&includeArchived=true"
	if got != want {
		t.Fatalf("RedactQuery = %q, want %q", got, want)
	}
	if strings.Contains(got, redactTicket) {
		t.Fatal("ticket value survived redaction")
	}
}

func TestRedactQueryRepeatedAndCaseInsensitive(t *testing.T) {
	got := RedactQuery("Ticket=a&TICKET=b&ticket=c&x=1")
	for _, v := range []string{"a", "b", "c"} {
		if strings.Contains(got, "="+v+"&") || strings.HasSuffix(got, "="+v) {
			t.Fatalf("sensitive value %q survived: %q", v, got)
		}
	}
	if !strings.Contains(got, "x=1") {
		t.Fatalf("non-sensitive pair lost: %q", got)
	}
	if n := strings.Count(got, "%5Bredacted%5D"); n != 3 {
		t.Fatalf("expected every repeated sensitive value redacted exactly once each, got %d in %q", n, got)
	}
}

func TestRedactQueryPreservesMalformedEncoding(t *testing.T) {
	raw := "name=%ZZbroken&c=secret&q=a%20b"
	got := RedactQuery(raw)
	if !strings.Contains(got, "name=%ZZbroken") {
		t.Fatalf("malformed sequence was altered: %q", got)
	}
	if !strings.Contains(got, "q=a%20b") {
		t.Fatalf("encoded non-sensitive value altered: %q", got)
	}
	if strings.Contains(got, "secret") {
		t.Fatalf("sensitive value survived: %q", got)
	}
}

func TestRedactURLString(t *testing.T) {
	got := RedactURLString("/api/v1/realtime/sse?ticket=" + redactTicket + "&circleId=3")
	if strings.Contains(got, redactTicket) {
		t.Fatalf("ticket leaked: %q", got)
	}
	if !strings.Contains(got, "/api/v1/realtime/sse") || !strings.Contains(got, "circleId=3") {
		t.Fatalf("useful diagnostics lost: %q", got)
	}

	// Legacy password-reset link.
	got = RedactURLString("https://host/reset?c=" + url.QueryEscape(redactCode) + "&x=y")
	if strings.Contains(got, redactCode) {
		t.Fatalf("reset code leaked: %q", got)
	}

	// userinfo credentials must not be echoed.
	got = RedactURLString("https://user:supersecret@host/path?a=b")
	if strings.Contains(got, "supersecret") || strings.Contains(got, "user:") {
		t.Fatalf("userinfo leaked: %q", got)
	}
}

func TestRedactURLStringMalformedFailsSafe(t *testing.T) {
	// A control character makes net/url refuse to parse.
	for _, raw := range []string{"http://ex\x7fample.com/?ticket=x", "%zz", ""} {
		got := RedactURLString(raw)
		if raw == "" {
			continue // empty parses fine and yields "/"
		}
		if got == raw {
			t.Fatalf("raw unparsable URL echoed back: %q", got)
		}
	}
	if got := RedactURLString("ht tp://bad url/?code=1"); got == "ht tp://bad url/?code=1" {
		t.Fatalf("unparsable URL echoed back verbatim")
	}
	if got := RedactURL(nil); got != "[nil-url]" {
		t.Fatalf("nil URL handling: %q", got)
	}
}

func TestRedactQueryEmpty(t *testing.T) {
	if got := RedactQuery(""); got != "" {
		t.Fatalf("empty query: %q", got)
	}
	if got := RedactQuery("&&"); got != "&&" {
		t.Fatalf("degenerate query should pass through: %q", got)
	}
}
