package events

import (
	"bytes"
	"testing"
	"time"
)

const testSigningSecret = "whsec_test_signing_key_0123456789"

var vectorBody = []byte(`{"type":"task.created","timestamp":"2024-01-01T00:00:00Z"}`)

// Deterministic HMAC-SHA256 vector for the canonical input
// "1700000000." + exact raw body, secret whsec_test_signing_key_0123456789.
const vectorSig = "eb1ad351ac34bb66dcd85d45632a97f58dc8f887038846a90d054b3a8d4d239c"

func TestSignatureDeterministicVector(t *testing.T) {
	s := newWebhookSigner(testSigningSecret)
	ts := time.Unix(1700000000, 0)
	if got := s.computeSignature(ts, vectorBody); got != vectorSig {
		t.Fatalf("signature = %s, want %s", got, vectorSig)
	}
	header, tsHeader, _ := s.signatureHeader(ts, vectorBody)
	wantHeader := "t=1700000000,v1=" + vectorSig
	if header != wantHeader {
		t.Fatalf("header = %s, want %s", header, wantHeader)
	}
	if tsHeader != "1700000000" {
		t.Fatalf("timestamp header = %s", tsHeader)
	}
}

func TestVerifyAcceptsValidAndRejectsMutations(t *testing.T) {
	s := newWebhookSigner(testSigningSecret)
	ts := time.Unix(1700000000, 0)
	header, _, _ := s.signatureHeader(ts, vectorBody)
	if !verifySignature(testSigningSecret, ts, vectorBody, header, time.Minute) {
		t.Fatal("valid signature failed verification")
	}

	// Body mutation.
	mutated := bytes.Replace(vectorBody, []byte("created"), []byte("updated"), 1)
	if verifySignature(testSigningSecret, time.Now(), mutated, header, 2*time.Hour) {
		t.Fatal("mutated body verified")
	}

	// Timestamp mutation: signature covers the timestamp, so shifting it in
	// the canonical input must invalidate the signature.
	if verifySignature(testSigningSecret, ts.Add(time.Hour), vectorBody, header, time.Minute) {
		t.Fatal("stale-timestamp delivery verified within tolerance")
	}

	// Wrong secret.
	if verifySignature("whsec_other_secret_99999999999", ts, vectorBody, header, time.Minute) {
		t.Fatal("wrong secret verified")
	}
}

func TestSignatureJSONFormattingSensitivity(t *testing.T) {
	// Two byte strings that decode to identical JSON must produce different
	// signatures: the canonical input is the EXACT raw body, so receivers
	// never depend on re-marshalling stability.
	a := []byte(`{"a":1,"b":2}`)
	b := []byte(`{"a": 1,"b":2}`)
	s := newWebhookSigner(testSigningSecret)
	ts := time.Unix(1700000000, 0)
	if s.computeSignature(ts, a) == s.computeSignature(ts, b) {
		t.Fatal("whitespace-only JSON difference produced identical signatures")
	}
}

func TestDeliveredRequestCarriesVerifiableSignature(t *testing.T) {
	var sigHeader, tsHeader string
	var body []byte
	serverURL, closeSrv := startTestServer(t, func(buf []byte, h map[string]string) {
		body = buf
		sigHeader = h["X-Dydan-Signature"]
		tsHeader = h["X-Dydan-Timestamp"]
	})
	defer closeSrv()

	p := newTestProducer(t)
	p.processEvent(Event{Type: EventTypeThingChanged, URL: serverURL, Data: map[string]any{"x": 1}})

	if sigHeader == "" || tsHeader == "" {
		t.Fatal("delivery missing signature/timestamp headers")
	}
	tsSecs := parseInt64(t, tsHeader)
	delivered := time.Unix(tsSecs, 0)
	if !verifySignature(p.signer.secretString(), delivered, body, sigHeader, time.Minute) {
		t.Fatal("delivered payload did not verify against its own headers")
	}
}

func (s *webhookSigner) secretString() string { return string(s.secret) }
