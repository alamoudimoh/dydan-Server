package utils

import (
	"regexp"
	"strings"
)

func ExtractImageURLs(markdown string) []string {
	var urls []string
	// Regex for ![alt](url)
	imgMD := regexp.MustCompile(`!\[[^\]]*\]\(([^)]+)\)`)
	// Regex for <img src="url">
	imgHTML := regexp.MustCompile(`<img[^>]*src=["']([^"'>]+)["'][^>]*>`)

	for _, match := range imgMD.FindAllStringSubmatch(markdown, -1) {
		if len(match) > 1 {
			urls = append(urls, match[1])
		}
	}
	for _, match := range imgHTML.FindAllStringSubmatch(markdown, -1) {
		if len(match) > 1 {
			urls = append(urls, match[1])
		}
	}
	return urls
}

// Image reference shapes covered by the sanitizer. Matching is deliberately
// broad and case-insensitive: descriptions are attacker-influenced content,
// so the sanitizer must not be bypassable by casing, quoting or attribute
// variations (src/srcset/data-src), nor by HTML-entity-encoded "?"
// ("&#63;" / "&#x3f;") which browsers still decode inside attribute values.
var (
	imgTagQuoted    = regexp.MustCompile(`(?i)(<img[^>]*?\b(?:src|srcset|data-src)=["'])([^"']+)(["'])`)
	imgTagUnquoted  = regexp.MustCompile(`(?i)(<img[^>]*?\b(?:src|srcset|data-src)=)([^\s"'>]+)`)
	imgMarkdownLink = regexp.MustCompile(`(!\[[^\]]*\]\()([^)\s]+)([^)]*\))`)
)

func hasURLScheme(raw string) bool {
	idx := strings.Index(raw, "://")
	return idx > 0
}

// sigv4QueryMarker detects AWS SigV4 presigned-URL parameters in a query
// fragment. These query values are bearer credentials (X-Amz-Signature /
// X-Amz-Credential) valid until expiry, regardless of whether the URL is
// relative or absolute.
func containsSigV4Credential(query string) bool {
	lower := strings.ToLower(query)
	return strings.Contains(lower, "x-amz-signature") ||
		strings.Contains(lower, "x-amz-credential")
}

// earliestQueryStart finds where a query begins, accounting for browsers
// decoding "&#63;" as "?" inside attribute values.
func earliestQueryStart(raw string) int {
	cut := -1
	for _, marker := range []string{"?", "&#63;", "&#x3f;", "&#X3F;"} {
		if idx := strings.Index(raw, marker); idx >= 0 && (cut < 0 || idx < cut) {
			cut = idx
		}
	}
	return cut
}

func stripAbsoluteCredentialQuery(raw string) string {
	cut := earliestQueryStart(raw)
	if cut < 0 {
		return raw
	}
	if containsSigV4Credential(raw[cut:]) {
		return raw[:cut]
	}
	return raw
}

func stripCredentialQuery(raw string) string {
	if hasURLScheme(raw) {
		// External image URLs are passed through untouched — unless they
		// carry SigV4 credentials: absolute S3 presigned URLs minted by the
		// storage signer are exactly as sensitive as relative ones.
		return stripAbsoluteCredentialQuery(raw)
	}
	cut := len(raw)
	if idx := strings.IndexByte(raw, '?'); idx >= 0 {
		cut = idx
	}
	// Entity-encoded "?" inside an attribute value: a browser rendering the
	// webhook payload would reconstruct the credential-bearing query.
	for _, enc := range []string{"&#63;", "&#x3f;", "&#X3F;"} {
		if idx := strings.Index(raw[:cut], enc); idx >= 0 && idx < cut {
			cut = idx
		}
	}
	return raw[:cut]
}

func stripFromMatch(parts []string, urlIndex int) string {
	if len(parts) <= urlIndex {
		return parts[0]
	}
	var b strings.Builder
	for i, part := range parts {
		switch i {
		case 0:
			continue
		case urlIndex:
			b.WriteString(stripCredentialQuery(part))
		default:
			b.WriteString(part)
		}
	}
	return b.String()
}

// StripMediaURLCredentials removes signed-media credential query strings
// from image references in rich text — relative and absolute alike. It is
// applied to chore payloads leaving for external webhook consumers:
// descriptions embed <img src="...?sig=..."> / SigV4 presigned URLs whose
// query strings are bearer credentials valid until expiry. The stable path
// is always preserved (dt-data-path / bare src); first-party API/sync
// channels are untouched because clients rely on the embedded expiry to
// re-sign. Non-SigV4 external URLs (third-party CDNs) are left unchanged.
func StripMediaURLCredentials(text string) string {
	if text == "" {
		return text
	}
	if !strings.Contains(text, "?") && !strings.Contains(text, "&#") &&
		!strings.Contains(text, "&#x") && !strings.Contains(text, "&#X") {
		return text
	}
	text = imgTagQuoted.ReplaceAllStringFunc(text, func(match string) string {
		return stripFromMatch(imgTagQuoted.FindStringSubmatch(match), 2)
	})
	text = imgTagUnquoted.ReplaceAllStringFunc(text, func(match string) string {
		return stripFromMatch(imgTagUnquoted.FindStringSubmatch(match), 2)
	})
	text = imgMarkdownLink.ReplaceAllStringFunc(text, func(match string) string {
		return stripFromMatch(imgMarkdownLink.FindStringSubmatch(match), 2)
	})
	return text
}
