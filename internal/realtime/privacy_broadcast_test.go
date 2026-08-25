package realtime

import (
	"fmt"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	chModel "github.com/alamoudimoh/daydan-server/internal/chore/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"go.uber.org/zap"
)

func newPrivacyTestService(t *testing.T) *RealTimeService {
	t.Helper()
	cfg := &config.Config{}
	cfg.RealTimeConfig.Enabled = true
	cfg.RealTimeConfig.MaxConnections = 100
	cfg.RealTimeConfig.MaxConnectionsPerUser = 10
	cfg.RealTimeConfig.EventQueueSize = 100
	cfg.RealTimeConfig.CleanupInterval = time.Minute
	cfg.RealTimeConfig.StaleThreshold = 5 * time.Minute
	svc := NewRealTimeService(cfg)
	if err := svc.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = svc.Stop() })
	return svc
}

func connectTestUser(t *testing.T, svc *RealTimeService, userID int) *Connection {
	t.Helper()
	conn := NewConnection(fmt.Sprintf("conn-test-%d", userID), 1, userID, &uModel.User{ID: userID}, nil, zap.NewNop().Sugar())
	if err := svc.AddConnection(conn); err != nil {
		t.Fatal(err)
	}
	return conn
}

func drainEvent(conn *Connection) *Event {
	select {
	case event := <-conn.Send:
		return event
	case <-time.After(200 * time.Millisecond):
		return nil
	}
}

func TestPrivateChoreEventsAreRestrictedToCreatorAndAssignees(t *testing.T) {
	svc := newPrivacyTestService(t)
	assignee := connectTestUser(t, svc, 2)
	outsider := connectTestUser(t, svc, 3)

	private := &chModel.Chore{
		ID:        42,
		CircleID:  1,
		Name:      "secret chore",
		IsPrivate: true,
		CreatedBy: 1,
		AssignedTo: &[]int{2}[0],
	}

	broadcaster := svc.GetEventBroadcaster()
	broadcaster.BroadcastChoreUpdated(private, &uModel.User{ID: 1}, map[string]interface{}{"status": "started"}, nil)

	if drainEvent(assignee) == nil {
		t.Fatal("assignee of a private chore must receive the update event")
	}
	if drainEvent(outsider) != nil {
		t.Fatal("circle member without visibility must NOT receive private chore payloads")
	}
}

func TestPublicChoreEventsStillReachWholeCircle(t *testing.T) {
	svc := newPrivacyTestService(t)
	memberA := connectTestUser(t, svc, 2)
	memberB := connectTestUser(t, svc, 3)

	public := &chModel.Chore{ID: 43, CircleID: 1, Name: "shared chore"}

	broadcaster := svc.GetEventBroadcaster()
	broadcaster.BroadcastChoreUpdated(public, &uModel.User{ID: 1}, map[string]interface{}{"status": "started"}, nil)

	if drainEvent(memberA) == nil || drainEvent(memberB) == nil {
		t.Fatal("public chore events must reach every circle member")
	}
}

func TestPrivateChoreSubtaskEventsAreRestricted(t *testing.T) {
	svc := newPrivacyTestService(t)
	assignee := connectTestUser(t, svc, 2)
	outsider := connectTestUser(t, svc, 3)

	private := &chModel.Chore{
		ID:        44,
		CircleID:  1,
		Name:      "secret chore",
		IsPrivate: true,
		CreatedBy: 1,
		AssignedTo: &[]int{2}[0],
	}

	broadcaster := svc.GetEventBroadcaster()
	broadcaster.BroadcastSubtaskUpdated(private, 7, nil, &uModel.User{ID: 1}, private.SyncVersion)
	broadcaster.BroadcastSubtaskCompleted(private, 7, nil, &uModel.User{ID: 1}, private.SyncVersion)

	if drainEvent(assignee) == nil || drainEvent(assignee) == nil {
		t.Fatal("assignee of a private chore must receive its subtask events")
	}
	if drainEvent(outsider) != nil {
		t.Fatal("subtask events for a private chore must not leak to members without visibility")
	}
}
