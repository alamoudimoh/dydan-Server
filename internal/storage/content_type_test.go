package storage

import "testing"

// Regression: uploaded files must be served with a conservative Content-Type
// plus nosniff so an HTML/SVG upload cannot render as an active document on
// the API origin (stored XSS).
func TestSafeContentTypeAllowlist(t *testing.T) {
	cases := map[string]string{
		"users/1/uuid.jpg":  "image/jpeg",
		"users/1/uuid.JPEG": "image/jpeg",
		"users/1/uuid.png":  "image/png",
		"users/1/uuid.gif":  "image/gif",
		"users/1/uuid.webp": "image/webp",
		"users/1/uuid.avif": "image/avif",
		"users/1/uuid.html": "application/octet-stream",
		"users/1/uuid.svg":  "application/octet-stream",
		"users/1/uuid.js":   "application/octet-stream",
		"users/1/uuid":      "application/octet-stream",
		"users/1/noext":     "application/octet-stream",
		"users/1/x.txt":     "application/octet-stream",
	}
	for name, want := range cases {
		if got := safeContentType(name); got != want {
			t.Errorf("safeContentType(%q) = %q, want %q", name, got, want)
		}
	}
}

// Regression: extension extraction must not panic on dotless filenames.
func TestFileExtHandlesDotlessNames(t *testing.T) {
	if got := fileExt("noext"); got != "" {
		t.Fatalf("fileExt(noext) = %q, want empty", got)
	}
	if got := fileExt("photo.JPG"); got != ".jpg" {
		t.Fatalf("fileExt(photo.JPG) = %q, want .jpg", got)
	}
	if got := fileExt("archive.tar.gz"); got != ".gz" {
		t.Fatalf("fileExt(archive.tar.gz) = %q, want .gz", got)
	}
}
