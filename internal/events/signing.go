package events

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
	"time"
)

// Outbound webhook authenticity contract (docs/webhook-signing.md).
//
// Every DayDan-generated webhook carries:
//
//	X-Daydan-Signature: t=<unix-seconds>,v1=<hex(HMAC-SHA256(secret, canonical))>
//	X-Daydan-Timestamp: <unix-seconds>
//
// The canonical signing input is exactly
//
//	timestamp-decimal + "." + exact-raw-request-body
//
// Receivers MUST verify against the raw bytes they received (never a
// re-marshalled copy), recompute the HMAC with their configured secret,
// compare with hmac.Equal (constant time), and reject timestamps older than
// their freshness window. There is deliberately no unsigned legacy mode.

const (
	signaturePrefixV1 = "v1="
)

type webhookSigner struct {
	secret []byte
}

func newWebhookSigner(secret string) *webhookSigner {
	return &webhookSigner{secret: []byte(secret)}
}

// canonicalSigningInput renders the stable byte string covered by the
// signature: "<timestamp>.<raw body>".
func canonicalSigningInput(timestamp time.Time, body []byte) []byte {
	prefix := strconv.FormatInt(timestamp.Unix(), 10) + "."
	out := make([]byte, 0, len(prefix)+len(body))
	out = append(out, prefix...)
	return append(out, body...)
}

// computeSignature returns the hex-encoded HMAC-SHA256 over the canonical
// input.
func (s *webhookSigner) computeSignature(timestamp time.Time, body []byte) string {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(canonicalSigningInput(timestamp, body))
	return hex.EncodeToString(mac.Sum(nil))
}

// signatureHeader renders the delivery headers. X-Daydan-Signature carries
// both parts ("t=<unix>,v1=<hex>") so verification is self-contained;
// X-Daydan-Timestamp duplicates the timestamp for convenience parsing.
func (s *webhookSigner) signatureHeader(now time.Time, body []byte) (string, string, string) {
	ts := now.UTC()
	sig := s.computeSignature(ts, body)
	header := "t=" + strconv.FormatInt(ts.Unix(), 10) + "," + signaturePrefixV1 + sig
	return header, strconv.FormatInt(ts.Unix(), 10), sig
}

// verifySignature is the receiver-side reference implementation (used by the
// deterministic test vectors and available to any Go consumer). It performs
// a constant-time comparison.
func verifySignature(secret string, now time.Time, body []byte, headerValue string, tolerance time.Duration) bool {
	idx := strings.Index(headerValue, ",")
	if idx < 0 || !strings.HasPrefix(headerValue, "t=") {
		return false
	}
	tsPart := strings.TrimPrefix(headerValue[:idx], "t=")
	sigPart := strings.TrimPrefix(headerValue[idx+1:], signaturePrefixV1)
	tsUnix, err := strconv.ParseInt(tsPart, 10, 64)
	if err != nil {
		return false
	}
	delivered := time.Unix(tsUnix, 0)
	if delivered.Before(now.Add(-tolerance)) || delivered.After(now.Add(tolerance)) {
		return false
	}
	signer := newWebhookSigner(secret)
	expected, _ := hex.DecodeString(signer.computeSignature(delivered.UTC(), body))
	got, err := hex.DecodeString(sigPart)
	if err != nil {
		return false
	}
	return hmac.Equal(expected, got)
}
