package discord

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	nModel "github.com/alamoudimoh/daydan-server/internal/notifier/model"
)

// TestSendMessageRejectsInsecureAndPrivateDestinations proves the
// user-controlled notification target cannot pivot the server into internal
// infrastructure or plaintext transport: private-network destinations and
// non-HTTPS schemes are refused by the shared outbound client before any
// request is issued.
func TestSendMessageRejectsInsecureAndPrivateDestinations(t *testing.T) {
	var hits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	dn := NewDiscordNotifier(nil)
	notification := &nModel.NotificationDetails{}
	notification.TargetID = server.URL // http://127.0.0.1:<port>
	if err := dn.SendNotification(context.Background(), notification); err == nil {
		t.Fatal("private/insecure destination was accepted")
	}
	if got := hits.Load(); got != 0 {
		t.Fatalf("destination received %d requests, want 0", got)
	}
}

// TestSendMessageRejectsGarbageTargets ensures malformed targets fail fast
// instead of producing confusing transport errors later.
func TestSendMessageRejectsGarbageTargets(t *testing.T) {
	for _, target := range []string{"not-a-url", "file:///etc/passwd", "ftp://example.com/x"} {
		dn := NewDiscordNotifier(nil)
		notification := &nModel.NotificationDetails{}
		notification.TargetID = target
		notification.Text = "hello"
		if err := dn.SendNotification(context.Background(), notification); err == nil {
			t.Fatalf("target %q was accepted", target)
		}
	}
}
