package events

import (
	"testing"
	"time"
)

// BenchmarkWebhookSigning quantifies the per-delivery cost of the Phase 8
// HMAC-SHA256 signature so it is demonstrably negligible on the delivery path.
func BenchmarkWebhookSigning(b *testing.B) {
	s := newWebhookSigner("bench-secret-not-used-in-prod")
	body := make([]byte, 2048)
	for i := range body {
		body[i] = byte(i % 251)
	}
	now := time.Now()
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if s.computeSignature(now, body) == "" {
			b.Fatal("empty signature")
		}
	}
}

// BenchmarkVerifyWebhookSignature quantifies receiver-side verification
// (reference implementation) including hex decode and constant-time compare.
func BenchmarkVerifyWebhookSignature(b *testing.B) {
	const secret = "bench-secret-not-used-in-prod"
	body := make([]byte, 2048)
	for i := range body {
		body[i] = byte(i % 251)
	}
	now := time.Now()
	header, _, _ := newWebhookSigner(secret).signatureHeader(now, body)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if !verifySignature(secret, now.Add(time.Second), body, header, 5*time.Minute) {
			b.Fatal("signature should verify")
		}
	}
}
