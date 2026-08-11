package service

import (
	"context"
	"testing"
	"time"

	chModel "donetick.com/core/internal/chore/model"
	mModel "donetick.com/core/internal/meter/model"
	nModel "donetick.com/core/internal/notifier/model"
	nRepo "donetick.com/core/internal/notifier/repo"
	tModel "donetick.com/core/internal/thing/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func inboxServiceDB(t *testing.T) (*InboxService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&chModel.Chore{}, &mModel.Meter{}, &mModel.ChoreSchedule{}, &mModel.UpdateReminder{}, &tModel.ThingChore{}, &nModel.InboxNotification{}); err != nil {
		t.Fatal(err)
	}
	repo := nRepo.NewInboxRepository(db)
	return NewInboxService(db, repo), db
}

func TestGenerateScheduledTaskDedupAndNextOccurrence(t *testing.T) {
	s, db := inboxServiceDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 15, 0, 0, 0, time.UTC)
	uid := 42
	due := now.Add(-time.Minute)
	ch := chModel.Chore{Name: "Weekly Review", CircleID: 7, AssignedTo: &uid, NextDueDate: &due, IsActive: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.GenerateScheduled(ctx, now); err != nil {
		t.Fatal(err)
	}
	if err := s.GenerateScheduled(ctx, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var rows []nModel.InboxNotification
	db.Find(&rows)
	if len(rows) != 1 || rows[0].Type != "task_due" {
		t.Fatalf("repeated scheduler created %+v", rows)
	}
	next := due.Add(7 * 24 * time.Hour)
	db.Model(&ch).Update("next_due_date", next)
	if err := s.GenerateScheduled(ctx, next.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	db.Order("id").Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("next recurrence suppressed: %+v", rows)
	}
}

func TestGenerateScheduledMeterMaintenanceTransitionsAndReasons(t *testing.T) {
	s, db := inboxServiceDB(t)
	ctx := context.Background()
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	uid := 8
	meter := mModel.Meter{CircleID: 3, CreatedBy: uid, Name: "Camry mileage", Unit: mModel.UnitKilometers, CurrentReading: 99300, CreatedAt: now, UpdatedAt: now}
	db.Create(&meter)
	ch := chModel.Chore{Name: "Oil Change", CircleID: 3, AssignedTo: &uid, IsActive: true}
	db.Create(&ch)
	schedule := mModel.ChoreSchedule{ChoreID: ch.ID, MeterID: meter.ID, Interval: 10000, WarnBefore: 1000, Baseline: 90000}
	db.Create(&schedule)
	if err := s.GenerateScheduled(ctx, now); err != nil {
		t.Fatal(err)
	}
	meter.CurrentReading = 99500
	db.Save(&meter)
	s.GenerateScheduled(ctx, now.Add(time.Minute))
	var rows []nModel.InboxNotification
	db.Order("id").Find(&rows)
	if len(rows) != 1 || rows[0].Type != "maintenance_approaching" {
		t.Fatalf("approaching spam/state: %+v", rows)
	}
	meter.CurrentReading = 100050
	db.Save(&meter)
	s.GenerateScheduled(ctx, now.Add(2*time.Minute))
	db.Order("id").Find(&rows)
	if len(rows) != 2 || rows[1].Type != "maintenance_due" || rows[1].Message != "Meter threshold reached" {
		t.Fatalf("due transition/reason: %+v", rows)
	}
}
