package events

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	chModel "github.com/alamoudimoh/daydan-server/internal/chore/model"
	"github.com/alamoudimoh/daydan-server/internal/httpx"
	"github.com/alamoudimoh/daydan-server/internal/outbound"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/alamoudimoh/daydan-server/internal/utils"
	"github.com/alamoudimoh/daydan-server/logging"
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

type EventsProducer struct {
	client *outbound.Client
	queue  chan Event
	logger *zap.SugaredLogger
	base   context.Context
}

func (p *EventsProducer) Start(ctx context.Context) {

	p.logger = logging.FromContext(ctx)
	p.base = ctx

	go func() {
		for event := range p.queue {
			p.processEvent(event)
		}
	}()
}

func NewEventsProducer(cfg *config.Config) *EventsProducer {
	timeout := cfg.WebhookConfig.Timeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &EventsProducer{
		client: outbound.NewClient(outbound.ClientOptions{
			Timeout:              timeout,
			AllowPrivateNetworks: cfg.WebhookConfig.AllowPrivateNetworks,
		}),
		queue: make(chan Event, cfg.WebhookConfig.QueueSize),
	}
}

func (p *EventsProducer) publishEvent(event Event) {
	select {
	case p.queue <- event:
		// Successfully added to queue
	default:
		log.Println("Webhook queue is full, dropping event")
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

func (p *EventsProducer) processEvent(event Event) {
	redactedURL := httpx.RedactURLString(event.URL)
	p.logger.Debugw("Sending webhook event", "type", event.Type, "url", redactedURL)

	if choreData, ok := event.Data.(ChoreData); ok {
		event.Data = p.sanitizeWebhookChore(choreData)
	}
	eventJSON, err := json.Marshal(event)
	if err != nil {
		p.logger.Errorw("Failed to marshal webhook event", "error", err)
		return
	}

	ctx := p.base
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, METHOD_POST, event.URL, bytes.NewReader(eventJSON))
	if err != nil {
		p.logger.Errorw("Failed to create webhook request", "error", err)
		return
	}
	req.Header.Set(HEAD_CONTENT_TYPE, CONTENT_TYPE_JSON)

	resp, err := p.client.Do(req)
	if err != nil {
		p.logger.Debugw("Failed to send webhook event", "url", redactedURL, "error", err)
		return
	}
	defer resp.Body.Close()

	_, _ = io.CopyN(io.Discard, resp.Body, maxWebhookResponseDrain)

	if resp.StatusCode >= 400 {
		p.logger.Errorw("Webhook request failed", "status", resp.StatusCode)
		return
	}
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
