package user

import (
	"context"
	"path/filepath"
	"sync"
	"testing"
	"time"

	nModel "donetick.com/core/internal/notifier/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func inboxDB(t *testing.T, dsn string) (*InboxRepository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&nModel.InboxNotification{}); err != nil {
		t.Fatal(err)
	}
	return NewInboxRepository(db), db
}
func note(uid, cid int, key string, at time.Time) *nModel.InboxNotification {
	return &nModel.InboxNotification{UserID: uid, CircleID: cid, Type: "task_due", Severity: "warning", Title: key, DedupKey: key, CreatedAt: at}
}

func TestInboxPersonalOrderingReadAndPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "inbox.db")
	r, db := inboxDB(t, path)
	ctx := context.Background()
	now := time.Now().UTC()
	for _, n := range []*nModel.InboxNotification{note(1, 10, "old", now.Add(-time.Hour)), note(1, 10, "new", now), note(2, 10, "other-user", now), note(1, 20, "other-circle", now)} {
		if _, err := r.Create(ctx, n); err != nil {
			t.Fatal(err)
		}
	}
	items, err := r.List(ctx, 1, 10, 30, 0)
	if err != nil || len(items) != 2 || items[0].Title != "new" {
		t.Fatalf("personal newest-first list: %+v %v", items, err)
	}
	count, _ := r.UnreadCount(ctx, 1, 10)
	if count != 2 {
		t.Fatalf("unread=%d", count)
	}
	if changed, _ := r.MarkRead(ctx, items[0].ID, 2, 10); changed {
		t.Fatal("forged user mutated notification")
	}
	if changed, _ := r.MarkRead(ctx, items[0].ID, 1, 20); changed {
		t.Fatal("forged circle mutated notification")
	}
	if changed, err := r.MarkRead(ctx, items[0].ID, 1, 10); err != nil || !changed {
		t.Fatal("mark read failed")
	}
	count, _ = r.UnreadCount(ctx, 1, 10)
	if count != 1 {
		t.Fatalf("unread after read=%d", count)
	}
	r.MarkAllRead(ctx, 1, 10)
	count, _ = r.UnreadCount(ctx, 1, 10)
	if count != 0 {
		t.Fatalf("unread after all=%d", count)
	}
	sqlDB, _ := db.DB()
	sqlDB.Close()
	r2, _ := inboxDB(t, path)
	persisted, _ := r2.List(ctx, 1, 10, 30, 0)
	if len(persisted) != 2 || persisted[0].ReadAt == nil || persisted[1].ReadAt == nil {
		t.Fatalf("restart did not preserve read state: %+v", persisted)
	}
}

func TestInboxDedupConcurrentAndOccurrenceAware(t *testing.T) {
	r, db := inboxDB(t, "file:"+t.Name()+"?mode=memory&cache=shared&_pragma=busy_timeout(5000)")
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = r.Create(ctx, note(1, 10, "task_due:1:7:2026-08-14", time.Now().UTC()))
		}()
	}
	wg.Wait()
	var count int64
	db.Model(&nModel.InboxNotification{}).Count(&count)
	if count != 1 {
		t.Fatalf("duplicate rows=%d", count)
	}
	created, err := r.Create(ctx, note(1, 10, "task_due:1:7:2026-08-21", time.Now().UTC()))
	if err != nil || !created {
		t.Fatalf("next occurrence suppressed: %v", err)
	}
}
