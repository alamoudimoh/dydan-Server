package utils

import (
	"strings"
	"testing"
)

// TestStripMediaURLCredentials proves persisted rich text never keeps minted
// media credentials, while stable paths and external URLs are untouched.
func TestStripMediaURLCredentials(t *testing.T) {
	local := `<img dt-data-path="users/1/a.jpg" src="users/1/a.jpg?expires=123&sig=abcdef">`
	got := StripMediaURLCredentials(local)
	if strings.Contains(got, "sig=") || strings.Contains(got, "expires=123") {
		t.Fatalf("local media credentials survived sanitization: %s", got)
	}
	if !strings.Contains(got, `src="users/1/a.jpg"`) {
		t.Fatalf("stable path must be preserved: %s", got)
	}
	if !strings.Contains(got, `dt-data-path="users/1/a.jpg"`) {
		t.Fatalf("dt-data-path must be preserved: %s", got)
	}

	sigv4 := `<img src="/uploads/users/1/b.png?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Signature=deadbeef&X-Amz-Expires=604800">`
	got = StripMediaURLCredentials(sigv4)
	if strings.Contains(got, "X-Amz-Signature") || strings.Contains(got, "AWS4") {
		t.Fatalf("SigV4 credentials survived sanitization: %s", got)
	}

	external := `<img src="https://cdn.example.com/i/logo.png?v=2&token=x">`
	if got := StripMediaURLCredentials(external); got != external {
		t.Fatalf("external image URLs must not be modified: %s", got)
	}

	markdown := `![pic](users/1/c.jpg?expires=9&sig=zZ)`
	got = StripMediaURLCredentials(markdown)
	if strings.Contains(got, "?expires") || strings.Contains(got, "sig=zZ") {
		t.Fatalf("markdown image credentials survived: %s", got)
	}
	if !strings.Contains(got, "(users/1/c.jpg)") {
		t.Fatalf("markdown path must be preserved: %s", got)
	}

	plain := `<p>no images here? fine &amp; ok</p>`
	if got := StripMediaURLCredentials(plain); got != plain {
		t.Fatalf("non-asset text must pass through unchanged: %s", got)
	}
}

// TestStripMediaURLCredentialsBypassShapes covers adversarial encodings that
// must not smuggle credentials through webhook payloads.
func TestStripMediaURLCredentialsBypassShapes(t *testing.T) {
	leaks := []string{
		`<IMG SRC="users/1/a.jpg?expires=1&sig=SECRET">`,
		`<img SRC=users/1/a.jpg?sig=SECRET>`,
		`<img srcset="users/1/a.jpg?sig=SECRET 1x, users/1/b.jpg 2x">`,
		`<img data-src="users/1/a.jpg?sig=SECRET">`,
		`<img src="users/1/a.jpg&#63;expires=1&#38;sig=SECRET">`,
		`![pic](users/1/a.jpg?sig=SECRET "title")`,
	}
	for _, in := range leaks {
		got := StripMediaURLCredentials(in)
		if strings.Contains(strings.ToUpper(got), "SECRET") {
			t.Errorf("credential survived: %s -> %s", in, got)
		}
	}

	preserved := []string{
		`<img dt-data-path="users/1/a.jpg" src="users/1/a.jpg">`,
		`![pic](https://cdn.example.com/i.png?token=keepme)`,
	}
	for _, in := range preserved {
		if got := StripMediaURLCredentials(in); got != in {
			t.Errorf("benign content modified: %s -> %s", in, got)
		}
	}
}
