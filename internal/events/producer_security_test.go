package events

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	chModel "github.com/alamoudimoh/daydan-server/internal/chore/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/alamoudimoh/daydan-server/logging"
)

func testProducer(t *testing.T, allowPrivate bool) *EventsProducer {
	t.Helper()
	cfg := config.NewConfig()
	cfg.WebhookConfig.Timeout = time.Second
	cfg.WebhookConfig.QueueSize = 10
	cfg.WebhookConfig.AllowPrivateNetworks = allowPrivate
	p := NewEventsProducer(cfg)
	p.logger = logging.FromContext(context.Background())
	return p
}

func TestWebhookRejectsLoopbackByDefault(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	p := testProducer(t, false)
	p.processEvent(Event{Type: EventTypeThingChanged, URL: server.URL, Data: map[string]any{"x": 1}})
	if calls.Load() != 0 {
		t.Fatal("loopback webhook was reached without private-network opt-in")
	}
}

func TestWebhookPrivateNetworkCompatibilityOptIn(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	p := testProducer(t, true)
	p.processEvent(Event{Type: EventTypeThingChanged, URL: server.URL, Data: map[string]any{"x": 1}})
	if calls.Load() != 1 {
		t.Fatalf("private webhook calls = %d, want 1", calls.Load())
	}
}

func TestPrivateChoreEventsAreNotPublished(t *testing.T) {
	p := testProducer(t, true)
	url := "https://example.test/webhook"
	chore := &chModel.Chore{ID: 7, IsPrivate: true}
	user := &uModel.User{ID: 2, Username: "private-user"}

	p.ChoreCreated(context.Background(), &url, chore, user)
	p.ChoreCompleted(context.Background(), &url, chore, user)
	p.ChoreSkipped(context.Background(), &url, chore, user)
	if got := len(p.queue); got != 0 {
		t.Fatalf("private chore queued %d webhook events", got)
	}
}

func TestWebhookSanitizesAbsoluteS3Credentials(t *testing.T) {
	var body string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		body = string(raw)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	description := `<img src="https://s3.example/bucket/a.png?X-Amz-Algorithm=AWS4-HMAC-SHA256&X-Amz-Credential=AKIA%2Fscope&X-Amz-Signature=SECRET">`
	p := testProducer(t, true)
	p.processEvent(Event{
		Type: EventTypeTaskCreated,
		URL:  server.URL,
		Data: ChoreData{Chore: &chModel.Chore{Description: &description}},
	})
	if strings.Contains(body, "SECRET") || strings.Contains(body, "X-Amz-Credential") {
		t.Fatalf("signed S3 credential leaked in webhook body: %s", body)
	}
}
