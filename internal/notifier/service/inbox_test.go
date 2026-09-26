package service

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	mModel "github.com/alamoudimoh/dydan-server/internal/meter/model"
	nModel "github.com/alamoudimoh/dydan-server/internal/notifier/model"
	nRepo "github.com/alamoudimoh/dydan-server/internal/notifier/repo"
	tModel "github.com/alamoudimoh/dydan-server/internal/thing/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type notificationEmailFake struct {
	calls []struct{ to, subject, body string }
	err   error
}

type notificationPushFake struct {
	calls []struct{ token, title, body string }
	err   error
}

func (f *notificationPushFake) SendInboxNotification(_ context.Context, token, title, body string, _ map[string]string) error {
	f.calls = append(f.calls, struct{ token, title, body string }{token, title, body})
	return f.err
}

func (f *notificationEmailFake) SendNotificationEmail(_ context.Context, to, subject, body string) error {
	f.calls = append(f.calls, struct{ to, subject, body string }{to, subject, body})
	return f.err
}

func inboxServiceDB(t *testing.T) (*InboxService, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&uModel.User{}, &uModel.UserPreference{}, &uModel.UserNotificationTarget{}, &cModel.UserCircle{}, &routineApplicationRecord{}, &routinePackRecord{}, &chModel.Chore{}, &chModel.ChoreHistory{}, &mModel.Meter{}, &mModel.ChoreSchedule{}, &mModel.UpdateReminder{}, &tModel.ThingChore{}, &nModel.InboxNotification{}); err != nil {
		t.Fatal(err)
	}
	repo := nRepo.NewInboxRepository(db)
	return NewInboxService(db, repo, &notificationEmailFake{}, &notificationPushFake{}), db
}

func inboxServiceWithEmail(t *testing.T, sender *notificationEmailFake) (*InboxService, *gorm.DB) {
	t.Helper()
	s, db := inboxServiceDB(t)
	s.emailSender = sender
	return s, db
}

func inboxServiceWithPush(t *testing.T, sender *notificationPushFake) (*InboxService, *gorm.DB) {
	t.Helper()
	s, db := inboxServiceDB(t)
	s.pushSender = sender
	return s, db
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

func TestNotificationEmailUsesVerifiedServerAccountAndIsIdempotent(t *testing.T) {
	sender := &notificationEmailFake{}
	s, db := inboxServiceWithEmail(t, sender)
	user := uModel.User{Username: "email-owner", Email: "owner@example.test", Timezone: "UTC"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&uModel.UserPreference{UserID: user.ID, Value: `{"notifications":{"taskReminders":true,"routineNudges":true,"familyActivity":true,"rewards":true,"weeklySummary":true,"quietHours":false,"pushChannel":false,"emailChannel":true}}`}).Error; err != nil {
		t.Fatal(err)
	}
	n := &nModel.InboxNotification{CircleID: 1, UserID: user.ID, Type: "task_due", Title: "Real task", Message: "Due now", DedupKey: "email-occurrence", CreatedAt: time.Now().UTC()}
	if err := s.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if err := s.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 1 || sender.calls[0].to != "owner@example.test" {
		t.Fatalf("email recipient/calls = %+v", sender.calls)
	}
}

func TestNotificationEmailHonorsSettingsAndProviderFailureIsolated(t *testing.T) {
	sender := &notificationEmailFake{}
	s, db := inboxServiceWithEmail(t, sender)
	user := uModel.User{Username: "email-options", Email: "verified@example.test", Timezone: "UTC"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	set := func(value string) {
		if err := db.Where("user_id = ?", user.ID).Assign(uModel.UserPreference{Value: value}).FirstOrCreate(&uModel.UserPreference{UserID: user.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	set(`{"notifications":{"taskReminders":true,"routineNudges":true,"familyActivity":true,"rewards":true,"weeklySummary":true,"quietHours":false,"pushChannel":false,"emailChannel":false}}`)
	if err := s.Notify(context.Background(), &nModel.InboxNotification{CircleID: 1, UserID: user.ID, Type: "task_due", Title: "t", DedupKey: "off", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 0 {
		t.Fatal("disabled email channel delivered")
	}
	set(`{"notifications":{"taskReminders":false,"routineNudges":true,"familyActivity":true,"rewards":true,"weeklySummary":true,"quietHours":false,"pushChannel":false,"emailChannel":true}}`)
	if err := s.Notify(context.Background(), &nModel.InboxNotification{CircleID: 1, UserID: user.ID, Type: "task_due", Title: "t", DedupKey: "category-off", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 0 {
		t.Fatal("disabled category delivered")
	}
	set(`{"notifications":{"taskReminders":true,"routineNudges":true,"familyActivity":true,"rewards":true,"weeklySummary":true,"quietHours":true,"pushChannel":false,"emailChannel":true}}`)
	if err := s.Notify(context.Background(), &nModel.InboxNotification{CircleID: 1, UserID: user.ID, Type: "task_due", Title: "t", DedupKey: "quiet", CreatedAt: now.Add(11 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if len(sender.calls) != 0 {
		t.Fatal("quiet hours delivered")
	}
	sender.err = errors.New("provider unavailable")
	if err := s.Notify(context.Background(), &nModel.InboxNotification{CircleID: 1, UserID: user.ID, Type: "task_due", Title: "t", DedupKey: fmt.Sprintf("failure-%d", now.Unix()), CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var rows []nModel.InboxNotification
	if err := db.Where("title = ?", "t").Find(&rows).Error; err != nil || len(rows) != 2 {
		t.Fatalf("email failure broke inbox: %v %+v", err, rows)
	}
}

func TestInboxNotificationPushHonorsChannelsCategoriesQuietHoursAndDedup(t *testing.T) {
	push := &notificationPushFake{}
	s, db := inboxServiceWithPush(t, push)
	user := uModel.User{Username: "push-owner", Email: "push@example.test", Timezone: "UTC"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&uModel.UserNotificationTarget{UserID: user.ID, Type: nModel.NotificationPlatformFCM, TargetID: "device-token"}).Error; err != nil {
		t.Fatal(err)
	}
	set := func(v string) {
		if err := db.Where("user_id = ?", user.ID).Assign(uModel.UserPreference{Value: v}).FirstOrCreate(&uModel.UserPreference{UserID: user.ID}).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	set(`{"notifications":{"taskReminders":true,"routineNudges":true,"familyActivity":true,"rewards":true,"weeklySummary":true,"quietHours":false,"pushChannel":true,"emailChannel":false}}`)
	n := &nModel.InboxNotification{CircleID: 1, UserID: user.ID, Type: "routine_due", Title: "Routine", Message: "Due", DedupKey: "push-routine", CreatedAt: now}
	if err := s.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if err := s.Notify(context.Background(), n); err != nil {
		t.Fatal(err)
	}
	if len(push.calls) != 1 || push.calls[0].token != "device-token" {
		t.Fatalf("routine push = %+v", push.calls)
	}
	set(`{"notifications":{"taskReminders":true,"routineNudges":true,"familyActivity":true,"rewards":true,"weeklySummary":true,"quietHours":false,"pushChannel":false,"emailChannel":true}}`)
	if err := s.Notify(context.Background(), &nModel.InboxNotification{CircleID: 1, UserID: user.ID, Type: "weekly_summary", Title: "Weekly", DedupKey: "push-off", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if len(push.calls) != 1 {
		t.Fatal("disabled push channel delivered")
	}
	set(`{"notifications":{"taskReminders":true,"routineNudges":true,"familyActivity":false,"rewards":true,"weeklySummary":true,"quietHours":false,"pushChannel":true,"emailChannel":true}}`)
	if err := s.Notify(context.Background(), &nModel.InboxNotification{CircleID: 1, UserID: user.ID, Type: "family_activity_completed", Title: "Family", DedupKey: "family-off", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if len(push.calls) != 1 {
		t.Fatal("disabled family category delivered push")
	}
	set(`{"notifications":{"taskReminders":true,"routineNudges":true,"familyActivity":true,"rewards":true,"weeklySummary":true,"quietHours":true,"pushChannel":true,"emailChannel":false}}`)
	if err := s.Notify(context.Background(), &nModel.InboxNotification{CircleID: 1, UserID: user.ID, Type: "weekly_summary", Title: "Quiet", DedupKey: "push-quiet", CreatedAt: now.Add(11 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if len(push.calls) != 1 {
		t.Fatal("quiet-hours push delivered")
	}
}

func TestLegacyNotificationPreferenceDefaultsMatchSettings(t *testing.T) {
	s, db := inboxServiceDB(t)
	push := &notificationPushFake{}
	emailSender := &notificationEmailFake{}
	s.pushSender = push
	s.emailSender = emailSender
	user := uModel.User{Username: "legacy-prefs", Email: "legacy@example.test", Timezone: "UTC"}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&uModel.UserNotificationTarget{UserID: user.ID, Type: nModel.NotificationPlatformFCM, TargetID: "default-device"}).Error; err != nil {
		t.Fatal(err)
	}

	p, exists, err := s.preferencesFor(context.Background(), user.ID)
	if err != nil || !exists {
		t.Fatalf("no-row preference: exists:%v err:%v", exists, err)
	}
	if !p.Notifications.TaskReminders || !p.Notifications.RoutineNudges || p.Notifications.FamilyActivity || !p.Notifications.Rewards || p.Notifications.WeeklySummary || !p.Notifications.PushChannel || p.Notifications.EmailChannel {
		t.Fatalf("canonical no-row defaults diverged: %+v", p.Notifications)
	}

	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	if err := s.Notify(context.Background(), &nModel.InboxNotification{CircleID: 1, UserID: user.ID, Type: "task_due", Title: "Default task", DedupKey: "default-task", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.Notify(context.Background(), &nModel.InboxNotification{CircleID: 1, UserID: user.ID, Type: "family_activity_completed", Title: "Default family", DedupKey: "default-family", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := s.Notify(context.Background(), &nModel.InboxNotification{CircleID: 1, UserID: user.ID, Type: "weekly_summary", Title: "Default weekly", DedupKey: "default-weekly", CreatedAt: now}); err != nil {
		t.Fatal(err)
	}
	var rows []nModel.InboxNotification
	if err := db.Order("id").Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 || rows[0].Type != "task_due" {
		t.Fatalf("no-row category defaults produced unexpected inbox rows: %+v", rows)
	}
	if len(push.calls) != 1 || push.calls[0].token != "default-device" {
		t.Fatalf("default push channel was not active: %+v", push.calls)
	}
	if len(emailSender.calls) != 0 {
		t.Fatalf("default email channel unexpectedly delivered: %+v", emailSender.calls)
	}

	if err := db.Create(&uModel.UserPreference{UserID: user.ID, Value: `{"notifications":{}}`}).Error; err != nil {
		t.Fatal(err)
	}
	p, exists, err = s.preferencesFor(context.Background(), user.ID)
	if err != nil || !exists {
		t.Fatalf("legacy preference: exists:%v err:%v", exists, err)
	}
	if !p.Notifications.TaskReminders || !p.Notifications.RoutineNudges || p.Notifications.FamilyActivity || !p.Notifications.Rewards || p.Notifications.WeeklySummary || !p.Notifications.PushChannel || p.Notifications.EmailChannel {
		t.Fatalf("canonical legacy defaults diverged: %+v", p.Notifications)
	}
	db.Model(&uModel.UserPreference{}).Where("user_id = ?", user.ID).Update("value", `{"notifications":{"emailChannel":true,"familyActivity":true,"weeklySummary":true}}`)
	p, _, err = s.preferencesFor(context.Background(), user.ID)
	if err != nil || !p.Notifications.EmailChannel || !p.Notifications.FamilyActivity || !p.Notifications.WeeklySummary {
		t.Fatalf("explicit legacy values lost: %+v err=%v", p.Notifications, err)
	}
}

func TestWeeklySummaryFridayTimezoneIsolationAndDeduplication(t *testing.T) {
	sender := &notificationEmailFake{}
	s, db := inboxServiceWithEmail(t, sender)
	owner := uModel.User{Username: "summary-owner", Email: "owner@example.test", Timezone: "America/Los_Angeles", CircleID: 9}
	other := uModel.User{Username: "summary-other", Email: "other@example.test", Timezone: "America/Los_Angeles", CircleID: 10}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&other).Error; err != nil {
		t.Fatal(err)
	}
	pref := `{"notifications":{"taskReminders":true,"routineNudges":true,"familyActivity":true,"rewards":true,"weeklySummary":true,"quietHours":false,"pushChannel":false,"emailChannel":true}}`
	if err := db.Create(&uModel.UserPreference{UserID: owner.ID, Value: pref}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&uModel.UserPreference{UserID: other.ID, Value: pref}).Error; err != nil {
		t.Fatal(err)
	}
	friday := time.Date(2026, 8, 14, 20, 0, 0, 0, time.UTC) // Friday afternoon in Los Angeles
	chore := chModel.Chore{Name: "Owner task", CircleID: owner.CircleID, CreatedBy: owner.ID, IsActive: true}
	if err := db.Create(&chore).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&chModel.ChoreHistory{ChoreID: chore.ID, CompletedBy: owner.ID, Status: chModel.ChoreHistoryStatusCompleted, PerformedAt: &friday}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.GenerateWeeklySummary(context.Background(), friday); err != nil {
		t.Fatal(err)
	}
	if err := s.GenerateWeeklySummary(context.Background(), friday.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	var rows []nModel.InboxNotification
	db.Where("type = ?", "weekly_summary").Find(&rows)
	if len(rows) != 2 {
		t.Fatalf("expected one digest per Friday-local user, got %+v", rows)
	}
	if len(sender.calls) != 2 || sender.calls[0].body != "You completed 1 task this week." {
		t.Fatalf("weekly email = %+v", sender.calls)
	}
	if err := s.GenerateWeeklySummary(context.Background(), friday.AddDate(0, 0, 3)); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&nModel.InboxNotification{}).Where("type = ?", "weekly_summary").Count(&count)
	if count != 2 {
		t.Fatalf("non-Friday generated a digest: %d", count)
	}
}

func TestRoutineNudgeAndFamilyActivityUseRealCircleEvents(t *testing.T) {
	sender := &notificationEmailFake{}
	s, db := inboxServiceWithEmail(t, sender)
	actor := uModel.User{Username: "actor", Email: "actor@test", CircleID: 4, Timezone: "UTC"}
	recipient := uModel.User{Username: "recipient", Email: "recipient@test", CircleID: 4, Timezone: "UTC"}
	otherCircle := uModel.User{Username: "other", Email: "other@test", CircleID: 5, Timezone: "UTC"}
	for _, user := range []*uModel.User{&actor, &recipient, &otherCircle} {
		if err := db.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	for _, m := range []cModel.UserCircle{{UserID: actor.ID, CircleID: 4, IsActive: true}, {UserID: recipient.ID, CircleID: 4, IsActive: true}, {UserID: otherCircle.ID, CircleID: 5, IsActive: true}} {
		if err := db.Create(&m).Error; err != nil {
			t.Fatal(err)
		}
	}
	pref := `{"notifications":{"taskReminders":true,"routineNudges":true,"familyActivity":true,"rewards":true,"weeklySummary":true,"quietHours":false,"pushChannel":true,"emailChannel":true}}`
	if err := db.Create(&uModel.UserPreference{UserID: recipient.ID, Value: pref}).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 14, 12, 0, 0, 0, time.UTC)
	due := now.Add(-time.Minute)
	routine := chModel.Chore{Name: "Morning routine", CircleID: 4, CreatedBy: actor.ID, AssignedTo: &recipient.ID, IsActive: true, NextDueDate: &due}
	if err := db.Create(&routine).Error; err != nil {
		t.Fatal(err)
	}
	pack := routinePackRecord{CircleID: 4, IsActive: true}
	if err := db.Create(&pack).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&routineApplicationRecord{CircleID: 4, PackID: pack.ID, ChoreIDs: []int{routine.ID}}).Error; err != nil {
		t.Fatal(err)
	}
	if err := s.GenerateScheduled(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	if err := s.GenerateScheduled(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	var routines []nModel.InboxNotification
	db.Where("type = ?", "routine_due").Find(&routines)
	if len(routines) != 1 || routines[0].UserID != recipient.ID {
		t.Fatalf("routine nudge mismatch: %+v", routines)
	}
	completion := &chModel.ChoreHistory{ID: 9001, ChoreID: routine.ID, CompletedBy: actor.ID, PerformedAt: &now, Status: chModel.ChoreHistoryStatusCompleted}
	if err := s.NotifyFamilyActivity(context.Background(), &routine, completion); err != nil {
		t.Fatal(err)
	}
	if err := s.NotifyFamilyActivity(context.Background(), &routine, completion); err != nil {
		t.Fatal(err)
	}
	var family []nModel.InboxNotification
	db.Where("type = ?", "family_activity_completed").Find(&family)
	if len(family) != 1 || family[0].UserID != recipient.ID {
		t.Fatalf("family notification leaked or duplicated: %+v", family)
	}
	db.Model(&uModel.UserPreference{}).Where("user_id = ?", recipient.ID).Update("value", `{"notifications":{"familyActivity":false,"routineNudges":true,"quietHours":false,"emailChannel":true}}`)
	completion.ID = 9002
	if err := s.NotifyFamilyActivity(context.Background(), &routine, completion); err != nil {
		t.Fatal(err)
	}
	db.Where("type = ?", "family_activity_completed").Find(&family)
	if len(family) != 1 {
		t.Fatalf("disabled recipient preference delivered: %+v", family)
	}
	if err := db.Create(&uModel.UserPreference{UserID: actor.ID, Value: `{"privacy":{"activityVisible":false}}`}).Error; err != nil {
		t.Fatal(err)
	}
	completion.ID = 9003
	if err := s.NotifyFamilyActivity(context.Background(), &routine, completion); err != nil {
		t.Fatal(err)
	}
	db.Where("type = ?", "family_activity_completed").Find(&family)
	if len(family) != 1 {
		t.Fatalf("hidden actor exposed family activity: %+v", family)
	}
	// Pending and rejected histories are not authoritative completions and must
	// never produce the final family event.
	completion.ID, completion.Status = 9004, chModel.ChoreHistoryStatusPendingApproval
	if err := s.NotifyFamilyActivity(context.Background(), &routine, completion); err != nil {
		t.Fatal(err)
	}
	completion.ID, completion.Status = 9005, chModel.ChoreHistoryStatusRejected
	if err := s.NotifyFamilyActivity(context.Background(), &routine, completion); err != nil {
		t.Fatal(err)
	}
	db.Where("type = ?", "family_activity_completed").Find(&family)
	if len(family) != 1 {
		t.Fatalf("non-final history emitted family activity: %+v", family)
	}
}
