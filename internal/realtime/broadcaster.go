package realtime

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	chModel "github.com/alamoudimoh/daydan-server/internal/chore/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
)

// EventBroadcaster handles broadcasting events to appropriate connections
type EventBroadcaster struct {
	service *RealTimeService
	config  *config.Config
}

// NewEventBroadcaster creates a new event broadcaster
func NewEventBroadcaster(service *RealTimeService, config *config.Config) *EventBroadcaster {
	return &EventBroadcaster{
		service: service,
		config:  config,
	}
}

// broadcastChoreEvent delivers a chore event to the chore's circle. Private
// chores are delivered only to the creator, assignees, and the current
// assignee target — the same participant set the REST/sync visibility rules
// (chModel.Chore.CanView) admit — so a full private-chore payload never fans
// out to every circle connection.
func (b *EventBroadcaster) broadcastChoreEvent(chore *chModel.Chore, event *Event) {
	if !chore.IsPrivate {
		b.service.BroadcastToCircle(chore.CircleID, event)
		return
	}
	allowed := map[int]bool{chore.CreatedBy: true}
	for _, assignee := range chore.Assignees {
		allowed[assignee.UserID] = true
	}
	if chore.AssignedTo != nil && *chore.AssignedTo != 0 {
		allowed[*chore.AssignedTo] = true
	}
	b.service.BroadcastToUsers(chore.CircleID, allowed, event)
}

// BroadcastChoreCreated broadcasts a chore creation event
func (b *EventBroadcaster) BroadcastChoreCreated(chore *chModel.Chore, user *uModel.User) {
	if !b.service.config.Enabled {
		return
	}

	event := NewChoreCreatedEvent(chore, user)
	event.ID = b.generateEventID()
	event.SyncVersion = chore.SyncVersion

	b.broadcastChoreEvent(chore, event)
}

// BroadcastChoreUpdated broadcasts a chore update event
func (b *EventBroadcaster) BroadcastChoreUpdated(chore *chModel.Chore, user *uModel.User, changes map[string]interface{}, note *string) {
	if !b.service.config.Enabled {
		return
	}

	event := NewChoreUpdatedEvent(chore, user, changes, note)
	event.ID = b.generateEventID()
	event.SyncVersion = chore.SyncVersion

	b.broadcastChoreEvent(chore, event)
}

// BroadcastChoreDeleted broadcasts a chore deletion event. The event carries
// the chore name, so for private chores it is restricted to the creator and
// assignees like all other chore events.
func (b *EventBroadcaster) BroadcastChoreDeleted(chore *chModel.Chore, user *uModel.User, syncVersion int64) {
	if !b.service.config.Enabled {
		return
	}

	event := NewChoreDeletedEvent(chore.ID, chore.Name, chore.CircleID, user)
	event.ID = b.generateEventID()
	event.SyncVersion = syncVersion

	b.broadcastChoreEvent(chore, event)
}

// BroadcastChoreCompleted broadcasts a chore completion event
func (b *EventBroadcaster) BroadcastChoreCompleted(chore *chModel.Chore, user *uModel.User, history *chModel.ChoreHistory, note *string) {
	if !b.service.config.Enabled {
		return
	}

	event := NewChoreCompletedEvent(chore, user, history, note)
	event.ID = b.generateEventID()
	event.SyncVersion = chore.SyncVersion
	if history != nil && history.SyncVersion > event.SyncVersion {
		event.SyncVersion = history.SyncVersion
	}

	b.broadcastChoreEvent(chore, event)
}

// BroadcastChoreStarted broadcasts a chore start event
func (b *EventBroadcaster) BroadcastChoreStatus(chore *chModel.Chore, user *uModel.User, changes map[string]interface{}) {
	if !b.service.config.Enabled {
		return
	}

	event := NewChoreStatusChangedEvent(chore, user, changes, nil)
	event.ID = b.generateEventID()
	event.SyncVersion = chore.SyncVersion

	b.broadcastChoreEvent(chore, event)
}

// BroadcastChoreSkipped broadcasts a chore skip event
func (b *EventBroadcaster) BroadcastChoreSkipped(chore *chModel.Chore, user *uModel.User, history *chModel.ChoreHistory, note *string) {
	if !b.service.config.Enabled {
		return
	}

	event := NewChoreSkippedEvent(chore, user, history, note)
	event.ID = b.generateEventID()
	event.SyncVersion = chore.SyncVersion
	if history != nil && history.SyncVersion > event.SyncVersion {
		event.SyncVersion = history.SyncVersion
	}

	b.broadcastChoreEvent(chore, event)
}

// BroadcastSubtaskUpdated broadcasts a subtask update event
// BroadcastSubtaskUpdated broadcasts a subtask update event. Subtask events
// reference their parent chore, so for private chores they are restricted to
// the same participant set as chore events — otherwise the event would leak
// the private chore's existence and activity cadence circle-wide.
func (b *EventBroadcaster) BroadcastSubtaskUpdated(chore *chModel.Chore, subtaskID int, completedAt *time.Time, user *uModel.User, syncVersion int64) {
	if !b.service.config.Enabled {
		return
	}

	event := NewSubtaskUpdatedEvent(chore.ID, subtaskID, completedAt, user, chore.CircleID)
	event.ID = b.generateEventID()
	event.SyncVersion = syncVersion

	b.broadcastChoreEvent(chore, event)
}

// BroadcastSubtaskCompleted broadcasts a subtask completion event with the
// same privacy scoping as BroadcastSubtaskUpdated.
func (b *EventBroadcaster) BroadcastSubtaskCompleted(chore *chModel.Chore, subtaskID int, completedAt *time.Time, user *uModel.User, syncVersion int64) {
	if !b.service.config.Enabled {
		return
	}

	event := NewSubtaskCompletedEvent(chore.ID, subtaskID, completedAt, user, chore.CircleID)
	event.ID = b.generateEventID()
	event.SyncVersion = syncVersion

	b.broadcastChoreEvent(chore, event)
}

// generateEventID generates a unique event ID
func (b *EventBroadcaster) generateEventID() string {
	bytes := make([]byte, 8)
	rand.Read(bytes)
	return hex.EncodeToString(bytes)
}
