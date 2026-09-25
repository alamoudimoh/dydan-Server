package service

import (
	"context"
	"testing"
	"time"

	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	mModel "github.com/alamoudimoh/dydan-server/internal/meter/model"
	nModel "github.com/alamoudimoh/dydan-server/internal/notifier/model"
	nRepo "github.com/alamoudimoh/dydan-server/internal/notifier/repo"
	tModel "github.com/alamoudimoh/dydan-server/internal/thing/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func inboxServiceDB(t *testing.T) (*InboxService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&uModel.User{}, &uModel.UserPreference{}, &chModel.Chore{}, &mModel.Meter{}, &mModel.ChoreSchedule{}, &mModel.UpdateReminder{}, &tModel.ThingChore{}, &nModel.InboxNotification{}); err != nil {
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

func TestNotifyHonorsPersistedCategoryAndQuietHourPreferences(t *testing.T) {
	s, db := inboxServiceDB(t)
	ctx := context.Background()
	user := uModel.User{Username: "preference-user", Email: "preference@example.test", Timezone: "UTC"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	pref := uModel.UserPreference{
		UserID: user.ID,
		Value:  `{"notifications":{"taskReminders":false,"routineNudges":true,"familyActivity":true,"rewards":true,"weeklySummary":true,"quietHours":true}}`,
	}
	if err := db.Create(&pref).Error; err != nil {
		t.Fatal(err)
	}

	daytime := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	if err := s.Notify(ctx, &nModel.InboxNotification{CircleID: 1, UserID: user.ID, Type: "task_due", Title: "Task", CreatedAt: daytime}); err != nil {
		t.Fatal(err)
	}
	if err := s.Notify(ctx, &nModel.InboxNotification{CircleID: 1, UserID: user.ID, Type: "routine_due", Title: "Routine", CreatedAt: daytime}); err != nil {
		t.Fatal(err)
	}
	if err := s.Notify(ctx, &nModel.InboxNotification{CircleID: 1, UserID: user.ID, Type: "routine_due", Title: "Quiet routine", CreatedAt: daytime.Add(11 * time.Hour)}); err != nil {
		t.Fatal(err)
	}

	var rows []nModel.InboxNotification
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Type != "routine_due" {
		t.Fatalf("preferences did not enforce category/quiet hours: %+v", rows)
	}
}

func TestExternalDeliveryHonorsCurrentCategoryChannelAndQuietHourPreferences(t *testing.T) {
	s, db := inboxServiceDB(t)
	ctx := context.Background()
	user := uModel.User{Username: "external-preference-user", Email: "external@example.test", Timezone: "UTC"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	pref := uModel.UserPreference{UserID: user.ID, Value: `{"notifications":{"taskReminders":true,"routineNudges":true,"familyActivity":true,"rewards":true,"weeklySummary":true,"quietHours":true,"pushChannel":false,"emailChannel":true}}`}
	if err := db.Create(&pref).Error; err != nil {
		t.Fatal(err)
	}
	daytime := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)

	allowed, err := s.AllowsExternalTaskDelivery(ctx, user.ID, nModel.NotificationPlatformFCM, daytime)
	if err != nil || allowed {
		t.Fatalf("disabled push channel was deliverable: allowed=%v err=%v", allowed, err)
	}
	allowed, err = s.AllowsExternalTaskDelivery(ctx, user.ID, nModel.NotificationPlatformTelegram, daytime)
	if err != nil || !allowed {
		t.Fatalf("enabled third-party task delivery was blocked: allowed=%v err=%v", allowed, err)
	}
	allowed, err = s.AllowsExternalTaskDelivery(ctx, user.ID, nModel.NotificationPlatformTelegram, daytime.Add(11*time.Hour))
	if err != nil || allowed {
		t.Fatalf("quiet-hours delivery was allowed: allowed=%v err=%v", allowed, err)
	}
	db.Model(&uModel.UserPreference{}).Where("user_id = ?", user.ID).Update("value", `{"notifications":{"taskReminders":false,"routineNudges":true,"familyActivity":true,"rewards":true,"weeklySummary":true,"quietHours":false,"pushChannel":true,"emailChannel":true}}`)
	allowed, err = s.AllowsExternalTaskDelivery(ctx, user.ID, nModel.NotificationPlatformTelegram, daytime)
	if err != nil || allowed {
		t.Fatalf("disabled task-reminder category was deliverable: allowed=%v err=%v", allowed, err)
	}
}
