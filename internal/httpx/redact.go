package httpx

import (
	"net/url"
	"strings"
)

// sensitiveQueryParams lists query-parameter names whose VALUES must never
// reach logs, traces, or panic diagnostics. Matching is case-insensitive on
// the percent-decoded name. Only values are replaced — parameter names and
// non-sensitive pairs are preserved byte-for-byte so diagnostics stay useful.
//
// The list is derived from Dydan's actual credential-bearing parameters:
//   - "ticket": single-use realtime/SSE connection tickets;
//   - "c":      legacy password-reset links (?c=<code>);
//   - "sig" / "X-Amz-Signature": signed media URL credentials;
//   - generic credential names that must never survive into diagnostics if a
//     future endpoint ever accepts them in a URL.
var sensitiveQueryParams = map[string]struct{}{
	"ticket":          {},
	"c":               {},
	"code":            {},
	"token":           {},
	"secretkey":       {},
	"password":        {},
	"refresh_token":   {},
	"access_token":    {},
	"id_token":        {},
	"sessiontoken":    {},
	"verifier":        {},
	"otp":             {},
	"mfa":             {},
	"sig":             {},
	"x-amz-signature": {},
}

const redactedQueryValue = "[redacted]"

func isSensitiveQueryParam(name string) bool {
	decoded, err := url.PathUnescape(name)
	if err != nil {
		decoded = name
	}
	_, ok := sensitiveQueryParams[strings.ToLower(decoded)]
	return ok
}

// RedactQuery returns rawQuery with the values of sensitive parameters
// replaced. It deliberately operates on the raw string instead of parsing and
// re-encoding through url.Values: malformed percent-sequences and unrelated
// diagnostic data pass through untouched, repeated parameters are each
// redacted, and parameter order is preserved.
func RedactQuery(rawQuery string) string {
	if rawQuery == "" {
		return ""
	}
	pairs := strings.Split(rawQuery, "&")
	for i, pair := range pairs {
		if pair == "" {
			continue
		}
		name := pair
		hasValue := false
		if idx := strings.IndexByte(pair, '='); idx >= 0 {
			name = pair[:idx]
			hasValue = true
		}
		if hasValue && isSensitiveQueryParam(name) {
			pairs[i] = name + "=" + url.QueryEscape(redactedQueryValue)
		}
	}
	return strings.Join(pairs, "&")
}

// RedactURLString redacts sensitive query values in an arbitrary URL string.
// On any parse failure it returns an opaque placeholder — the raw input is
// never echoed back, because unparsable URLs are exactly where attacker- or
// client-controlled bytes could hide credentials.
func RedactURLString(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[unparsable-url]"
	}
	return RedactURL(u)
}

// RedactURL renders u as path?query with sensitive query values redacted.
// userinfo/host are intentionally omitted: Dydan never needs them for
// debugging, and dropping them removes a class of accidental credential
// exposure (https://user:pass@host).
func RedactURL(u *url.URL) string {
	if u == nil {
		return "[nil-url]"
	}
	rendered := u.EscapedPath()
	if rendered == "" {
		rendered = "/"
	}
	if q := RedactQuery(u.RawQuery); q != "" {
		rendered += "?" + q
	}
	if u.Fragment != "" {
		rendered += "#" + u.Fragment
	}
	return rendered
}
