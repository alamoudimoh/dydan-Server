package events

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"math/rand"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	"github.com/alamoudimoh/dydan-server/internal/httpx"
	"github.com/alamoudimoh/dydan-server/internal/outbound"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/alamoudimoh/dydan-server/internal/utils"
	"github.com/alamoudimoh/dydan-server/logging"
	"go.uber.org/zap"
)

const (
	METHOD_POST       = "POST"
	HEAD_CONTENT_TYPE = "Content-Type"
	CONTENT_TYPE_JSON = "application/json"
)

type EventType string

const (
	EventTypeUnknown      EventType = ""
	EventTypeTaskCreated  EventType = "task.created"
	EventTypeTaskReminder EventType = "task.reminder"
	// EventTypeTaskUpdated    EventType = "task.updated"
	EventTypeTaskCompleted    EventType = "task.completed"
	EventTypeSubTaskCompleted EventType = "subtask.completed"
	// EventTypeTaskReassigned EventType = "task.reassigned"
	EventTypeTaskSkipped  EventType = "task.skipped"
	EventTypeThingChanged EventType = "thing.changed"
)

type Event struct {
	Type      EventType   `json:"type"`
	URL       string      `json:"-"`
	Timestamp time.Time   `json:"timestamp"`
	Data      interface{} `json:"data"`
}

type ChoreData struct {
	Chore       *chModel.Chore `json:"chore"`
	Username    string         `json:"username"`
	DisplayName string         `json:"display_name"`
	Note        string         `json:"note"`
}

// EventsProducer delivers Dydan-generated webhooks to external consumer
// URLs.
//
// Architecture (Phase 8):
//
//	inbound queue (global bound: QueueSize, drop-on-full backpressure)
//	  → dispatcher goroutine
//	    → per-tenant FIFO buckets (one per destination URL, bounded,
//	      single-flight per destination — tenant isolation)
//	      → worker goroutines (global fan-out capped at Workers)
//	        → signed delivery with bounded retries/backoff
//
// Ordering within one destination is FIFO best-effort (drops and retries can
// reorder); there are no cross-destination ordering guarantees.
type EventsProducer struct {
	client *outbound.Client
	queue  chan Event
	logger *zap.SugaredLogger
	base   context.Context

	// Dispatcher state.
	mu       sync.Mutex
	buckets  []*tenantBucket // round-robin ring of active destinations
	byKey    map[string]*tenantBucket
	cursor   int
	sem      chan struct{} // global concurrency slots
	idleMu   sync.Mutex
	idleCond *sync.Cond
	inflight int
	wake     chan struct{}

	// Delivery policy.
	signer       *webhookSigner
	maxAttempts  int
	backoffInit  time.Duration
	backoffMax   time.Duration
	perTenantCap int

	// Injectable clocks for deterministic tests.
	nowFn  func() time.Time
	sleepF func(context.Context, time.Duration) error
}

func (p *EventsProducer) perTenantLimit() int {
	if p.perTenantCap > 0 {
		return p.perTenantCap
	}
	return cap(p.queue) // default: same bound as the global queue
}

func (p *EventsProducer) notify() {
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

func (p *EventsProducer) now() time.Time {
	if p.nowFn != nil {
		return p.nowFn().UTC()
	}
	return time.Now().UTC()
}

func (p *EventsProducer) sleep(ctx context.Context, d time.Duration) error {
	if p.sleepF != nil {
		return p.sleepF(ctx, d)
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func redacted(url string) string { return httpx.RedactURLString(url) }

func (p *EventsProducer) Start(ctx context.Context) {

	p.logger = logging.FromContext(ctx)
	p.base = ctx

	go func() {
		<-ctx.Done()
		// Graceful shutdown: stop accepting new work (dispatcher exits on
		// ctx.Done) and let in-flight deliveries finish. Each delivery is
		// already bounded by the outbound client timeout, so this wait is
		// finite.
		p.waitForIdle()
	}()
	go p.dispatchLoop(ctx)
}

func NewEventsProducer(cfg *config.Config) *EventsProducer {
	timeout := cfg.WebhookConfig.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	maxAttempts := cfg.WebhookConfig.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	workers := cfg.WebhookConfig.Workers
	if workers <= 0 {
		workers = 8
	}
	signingSecret := cfg.WebhookConfig.SigningSecret
	if signingSecret == "" {
		// Config resolution normally guarantees a secret; an empty value in
		// unit tests must still produce deterministic (unsigned-safe)
		// behavior without panicking.
		signingSecret = "dydan-webhook-unsigned-dev-fallback"
	}
	queueSize := cfg.WebhookConfig.QueueSize
	if queueSize <= 0 {
		queueSize = 100
	}
	p := &EventsProducer{
		client: outbound.NewClient(outbound.ClientOptions{
			Timeout:              timeout,
			AllowPrivateNetworks: cfg.WebhookConfig.AllowPrivateNetworks,
		}),
		queue: make(chan Event, queueSize),
		byKey: make(map[string]*tenantBucket),
		sem:   make(chan struct{}, workers),
		wake:  make(chan struct{}, 1),

		signer:      newWebhookSigner(signingSecret),
		maxAttempts: maxAttempts,
		backoffInit: cfg.WebhookConfig.BackoffInitial,
		backoffMax:  cfg.WebhookConfig.BackoffMax,
	}
	p.idleCond = sync.NewCond(&p.idleMu)
	return p
}

func (p *EventsProducer) publishEvent(event Event) {
	select {
	case p.queue <- event:
		// Successfully added to queue
	default:
		if p.logger != nil {
			p.logger.Warnw("Webhook queue is full, dropping event",
				"type", string(event.Type), "url", redacted(event.URL))
		}
	}
}

// sanitizeWebhookChore strips minted signed-media credentials from a chore
// description before it leaves for an external webhook consumer. Descriptions
// embed <img src="...?sig=..."> URLs whose query strings are bearer
// credentials valid until the URL expires; third-party receivers have no
// business holding them. The stable path (dt-data-path / bare src) is kept,
// and first-party API/sync channels are untouched — clients rely on the
// embedded expiry to re-sign.
func (p *EventsProducer) sanitizeWebhookChore(data ChoreData) ChoreData {
	if data.Chore == nil || data.Chore.Description == nil {
		return data
	}
	sanitized := utils.StripMediaURLCredentials(*data.Chore.Description)
	data.Chore.Description = &sanitized
	return data
}

// maxWebhookResponseDrain bounds how much of a webhook response body is
// drained (for connection reuse) before closing. Consumer bodies are never
// interpreted, so an unbounded drain would only be an exhaustion vector.
const maxWebhookResponseDrain = 64 << 10

// processEvent performs a synchronous signed delivery of one event,
// including the bounded retry loop. Retained as the direct entry point for
// callers/tests that bypass the dispatcher.
func (p *EventsProducer) processEvent(event Event) {
	ctx := p.base
	if ctx == nil {
		ctx = context.Background()
	}
	p.deliverWithRetries(ctx, event)
}

// processEventContext delivers one event under an explicit context,
// bypassing the dispatcher (used by cancellation tests).
func (p *EventsProducer) processEventContext(ctx context.Context, event Event) {
	p.deliverWithRetries(ctx, event)
}

// slotOps hands the global concurrency slot to the delivery loop so it can
// be released while backing off: sleeping retriers must not consume delivery
// capacity that other tenants are waiting for. Nil for direct synchronous
// callers (which hold no slot).
type slotOps struct {
	acquire func(context.Context) bool
	release func()
}

// deliverWithRetries signs and delivers one event under the retry contract
// documented in retry.go.
func (p *EventsProducer) deliverWithRetries(ctx context.Context, event Event) {
	p.deliverWithRetriesSlot(ctx, event, nil)
}

func (p *EventsProducer) deliverWithRetriesSlot(ctx context.Context, event Event, slot *slotOps) {
	redactedURL := redacted(event.URL)
	if p.logger != nil {
		p.logger.Debugw("Sending webhook event", "type", event.Type, "url", redactedURL)
	} else {
		p.logger = logging.FromContext(ctx)
	}

	if choreData, ok := event.Data.(ChoreData); ok {
		event.Data = p.sanitizeWebhookChore(choreData)
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		p.logger.Errorw("Failed to marshal webhook event", "error", err)
		return
	}

	sigHeader, tsHeader, _ := p.signer.signatureHeader(p.now(), eventJSON)

	// Graceful-shutdown semantics: the retry LOOP honours ctx cancellation,
	// but an HTTP request already in flight finishes on a detached,
	// timeout-bounded context instead of being torn down mid-flight when the
	// server shuts down. The detached context is still bounded by the
	// outbound client's own timeout policy.
	reqCtx := context.WithoutCancel(ctx)

	for attempt := 0; ; attempt++ {
		req, err := http.NewRequestWithContext(reqCtx, METHOD_POST, event.URL, bytes.NewReader(eventJSON))
		if err != nil {
			// Malformed destination: never retryable.
			p.logger.Errorw("Failed to create webhook request", "url", redactedURL, "error", err)
			return
		}
		req.Header.Set(HEAD_CONTENT_TYPE, CONTENT_TYPE_JSON)
		req.Header.Set(signatureHeaderName, sigHeader)
		req.Header.Set(timestampHeaderName, tsHeader)
		req.Header.Set(legacySignatureHeaderName, sigHeader)
		req.Header.Set(legacyTimestampHeaderName, tsHeader)

		resp, err := p.client.Do(req)
		if err != nil {
			if classifyError(err) == outcomePermanent {
				p.logger.Errorw("Webhook destination rejected", "url", redactedURL, "error", err.Error())
				return
			}
			if !p.retryOrFailSlot(ctx, event, attempt, redactedURL, 0, slot) {
				return
			}
			continue
		}

		status := resp.StatusCode
		retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"), p.now(), p.backoffMax)
		_, _ = io.CopyN(io.Discard, resp.Body, maxWebhookResponseDrain)
		resp.Body.Close()

		switch classifyStatus(status) {
		case outcomeSuccess:
			p.logger.Debugw("Webhook delivered", "type", string(event.Type), "url", redactedURL, "status", status)
			return
		case outcomePermanent:
			p.logger.Errorw("Webhook request permanently rejected by receiver",
				"type", string(event.Type), "url", redactedURL, "status", status)
			return
		default:
			if !p.retryOrFailSlot(ctx, event, attempt, redactedURL, retryAfter, slot) {
				return
			}
		}
	}
}

// retryOrFailSlot releases the global slot while sleeping (so backoff never
// consumes delivery capacity), reacquires it afterwards, and reports whether
// another attempt should run. It returns false when attempts are exhausted,
// the context was cancelled, or the reacquire was aborted.
func (p *EventsProducer) retryOrFailSlot(ctx context.Context, event Event, attempt int, redactedURL string, retryAfter time.Duration, slot *slotOps) bool {
	next := attempt + 1
	if next >= p.maxAttempts {
		p.logger.Errorw("Webhook delivery abandoned after final attempt",
			"type", string(event.Type), "url", redactedURL, "attempts", p.maxAttempts)
		return false
	}
	delay := backoffDelay(p.backoffInit, p.backoffMax, next, rand.Float64())
	if retryAfter > delay {
		delay = retryAfter
	}
	p.logger.Warnw("Webhook delivery failed; retrying",
		"type", string(event.Type), "url", redactedURL,
		"next_attempt", next+1, "delay_ms", delay.Milliseconds())
	release := slot != nil && slot.release != nil
	if release {
		slot.release()
	}
	if err := sleepBackoff(ctx, p.sleepF, delay); err != nil {
		// Shutting down: drop the retry rather than block shutdown.
		p.logger.Warnw("Webhook retries cancelled during shutdown", "url", redactedURL)
		if release {
			slot.acquire(ctx)
		}
		return false
	}
	if release && !slot.acquire(ctx) {
		// Shutdown raced the reacquire: abandon the retry.
		return false
	}
	return true
}

// classifyError separates permanent destination-policy failures (SSRF guard,
// invalid URL — these will fail identically forever) from transient network
// errors worth retrying.
func classifyError(err error) outcome {
	msg := err.Error()
	for _, marker := range []string{
		"refusing to connect",
		"is not allowed",
		"unsupported scheme",
		"invalid URL",
		"URL has no host",
		"URL credentials",
		"URL fragment",
		"cross-host redirect",
		"stopped after",
		"no addresses for",
	} {
		if strings.Contains(msg, marker) {
			return outcomePermanent
		}
	}
	return outcomeTransient
}

// private chore data must not leave for a destination only circle admins
// control: admins cannot read another member's private chores through the
// API (Phase 5 boundary), so publishing them over the circle webhook would
// be an authorization bypass.
func (p *EventsProducer) withholdPrivate(chore *chModel.Chore) bool {
	if chore != nil && chore.IsPrivate {
		p.logger.Debug("Private chore event withheld from circle webhook")
		return true
	}
	return false
}

func (p *EventsProducer) ChoreCreated(ctx context.Context, webhookURL *string, chore *chModel.Chore, creator *uModel.User) {
	if webhookURL == nil || p.withholdPrivate(chore) {
		if webhookURL == nil {
			p.logger.Debug("No subscribers for circle, skipping webhook")
		}
		return
	}

	event := Event{
		Type:      EventTypeTaskCreated,
		URL:       *webhookURL,
		Timestamp: time.Now().UTC(),
		Data: ChoreData{
			Chore:       chore,
			Username:    creator.Username,
			DisplayName: creator.DisplayName,
		},
	}
	p.publishEvent(event)
}

func (p *EventsProducer) ChoreCompleted(ctx context.Context, webhookURL *string, chore *chModel.Chore, performer *uModel.User) {
	if webhookURL == nil || p.withholdPrivate(chore) {
		if webhookURL == nil {
			p.logger.Debug("No subscribers for circle, skipping webhook")
		}
		return
	}

	event := Event{
		Type:      EventTypeTaskCompleted,
		URL:       *webhookURL,
		Timestamp: time.Now().UTC(),
		Data: ChoreData{Chore: chore,
			Username:    performer.Username,
			DisplayName: performer.DisplayName,
		},
	}
	p.publishEvent(event)
}

func (p *EventsProducer) ChoreSkipped(ctx context.Context, webhookURL *string, chore *chModel.Chore, performer *uModel.User) {
	if webhookURL == nil || p.withholdPrivate(chore) {
		if webhookURL == nil {
			p.logger.Debug("No Webhook URL for circle, skipping webhook")
		}
		return
	}

	event := Event{
		Type:      EventTypeTaskSkipped,
		URL:       *webhookURL,
		Timestamp: time.Now().UTC(),
		Data: ChoreData{Chore: chore,
			Username:    performer.Username,
			DisplayName: performer.DisplayName,
		},
	}
	p.publishEvent(event)
}

func (p *EventsProducer) NotificationEvent(ctx context.Context, url string, event interface{}) {
	// print the event and the url :
	p.logger.Debug("Sending notification event")

	p.publishEvent(Event{
		URL:       url,
		Type:      EventTypeTaskReminder,
		Timestamp: time.Now().UTC(),
		Data:      event,
	})
}

func (p *EventsProducer) ThingsUpdated(ctx context.Context, url *string, data interface{}) {
	if url == nil {
		p.logger.Debug("No subscribers for circle, skipping webhook")
		return
	}
	p.publishEvent(Event{
		URL:       *url,
		Type:      EventTypeThingChanged,
		Timestamp: time.Now().UTC(),
		Data:      data,
	})
}

func (p *EventsProducer) SubtaskUpdated(ctx context.Context, url *string, data interface{}) {
	if url == nil {
		p.logger.Debug("No subscribers for circle, skipping webhook")
		return
	}
	p.publishEvent(Event{
		URL:       *url,
		Type:      EventTypeSubTaskCompleted,
		Timestamp: time.Now().UTC(),
		Data:      data,
	})
}
