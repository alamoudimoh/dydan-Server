package events

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"

	"testing"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

// TestSigningSecretNeverLogged proves the HMAC key never reaches logs even
// when deliveries fail and warn-level diagnostics fire.
func TestSigningSecretNeverLogged(t *testing.T) {
	core, logs := observer.New(zapcore.DebugLevel)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newTestProducer(t)
	p.logger = zap.New(core).Sugar()
	p.maxAttempts = 4
	p.sleepF = func(_ context.Context, _ time.Duration) error { return nil }
	p.processEvent(Event{Type: EventTypeThingChanged, URL: srv.URL, Data: map[string]any{"x": 1}})

	for _, e := range logs.All() {
		msg := e.Message
		for _, f := range e.Context {
			msg += " " + f.Key + "=" + fmt.Sprint(f.String)
		}
		if strings.Contains(msg, testSigningSecret) {
			t.Fatalf("signing secret leaked into log entry: %s", msg)
		}
	}
}
