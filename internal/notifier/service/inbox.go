package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	"github.com/alamoudimoh/dydan-server/internal/email"
	mModel "github.com/alamoudimoh/dydan-server/internal/meter/model"
	mRepo "github.com/alamoudimoh/dydan-server/internal/meter/repo"
	nModel "github.com/alamoudimoh/dydan-server/internal/notifier/model"
	nRepo "github.com/alamoudimoh/dydan-server/internal/notifier/repo"
	"github.com/alamoudimoh/dydan-server/internal/notifier/service/fcm"
	"github.com/alamoudimoh/dydan-server/internal/preferences"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"gorm.io/gorm"
)

type InboxService struct {
	db          *gorm.DB
	repo        *nRepo.InboxRepository
	emailSender email.NotificationSender
	pushSender  fcm.InboxPushSender
}

// routineApplicationRecord and routinePackRecord mirror only the persisted
// scheduling ownership fields. Keeping these local avoids a package cycle with
// the HTTP routine handler while still using its authoritative tables.
type routineApplicationRecord struct {
	CircleID int   `gorm:"column:circle_id"`
	PackID   int   `gorm:"column:pack_id"`
	ChoreIDs []int `gorm:"column:chore_ids;serializer:json"`
}

func (routineApplicationRecord) TableName() string { return "routine_applications" }

type routinePackRecord struct {
	ID       int  `gorm:"primaryKey"`
	CircleID int  `gorm:"column:circle_id"`
	IsActive bool `gorm:"column:is_active"`
}

func (routinePackRecord) TableName() string { return "routine_packs" }

func NewInboxService(db *gorm.DB, repo *nRepo.InboxRepository, sender email.NotificationSender, push fcm.InboxPushSender) *InboxService {
	return &InboxService{db: db, repo: repo, emailSender: sender, pushSender: push}
}

func (s *InboxService) Notify(ctx context.Context, n *nModel.InboxNotification) error {
	allowed, err := s.notificationAllowed(ctx, n)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	created, err := s.repo.Create(ctx, n)
	if err != nil {
		return err
	}
	// The durable unique dedup key is also the delivery idempotency boundary.
	// A scheduler retry/restart must not turn an already-created occurrence into
	// another email.
	if !created {
		return nil
	}
	if err := s.sendNotificationEmail(ctx, n); err != nil && !errors.Is(err, email.ErrDeliveryDisabled) {
		// Email is an independent channel. Inbox creation must remain available.
		// Continue to push: a failed email provider must not suppress it.
	}
	// Push failure is isolated from durable inbox/email delivery. `created` is
	// the occurrence idempotency boundary, so retries cannot duplicate it.
	_ = s.sendNotificationPush(ctx, n)
	return nil
}

func (s *InboxService) sendNotificationPush(ctx context.Context, n *nModel.InboxNotification) error {
	document, exists, err := s.preferencesFor(ctx, n.UserID)
	if err != nil || !exists || !document.Notifications.PushChannel || s.pushSender == nil {
		return err
	}
	if ok, err := s.quietHoursAllow(ctx, n.UserID, document.Notifications.QuietHours, n.CreatedAt); err != nil || !ok {
		return err
	}
	var target uModel.UserNotificationTarget
	if err := s.db.WithContext(ctx).Where("user_id = ? AND type = ?", n.UserID, nModel.NotificationPlatformFCM).First(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	return s.pushSender.SendInboxNotification(ctx, target.TargetID, n.Title, n.Message, map[string]string{"inboxNotificationID": fmt.Sprintf("%d", n.ID), "type": n.Type})
}

func (s *InboxService) sendNotificationEmail(ctx context.Context, n *nModel.InboxNotification) error {
	document, exists, err := s.preferencesFor(ctx, n.UserID)
	if err != nil || !exists || !document.Notifications.EmailChannel || s.emailSender == nil {
		return err
	}
	if ok, err := s.quietHoursAllow(ctx, n.UserID, document.Notifications.QuietHours, n.CreatedAt); err != nil || !ok {
		return err
	}
	var user uModel.User
	if err := s.db.WithContext(ctx).Select("email").First(&user, n.UserID).Error; err != nil {
		return err
	}
	if user.Email == "" {
		return email.ErrDeliveryDisabled
	}
	return s.emailSender.SendNotificationEmail(ctx, user.Email, n.Title, n.Message)
}

// notificationPreferenceDocument mirrors the persisted Settings document. It
// deliberately reads only delivery choices here: presentation choices remain
// owned by the account-settings handler, while the notifier enforces delivery.
type notificationPreferenceDocument struct {
	Notifications struct {
		TaskReminders  bool `json:"taskReminders"`
		RoutineNudges  bool `json:"routineNudges"`
		FamilyActivity bool `json:"familyActivity"`
		Rewards        bool `json:"rewards"`
		WeeklySummary  bool `json:"weeklySummary"`
		QuietHours     bool `json:"quietHours"`
		PushChannel    bool `json:"pushChannel"`
		EmailChannel   bool `json:"emailChannel"`
	} `json:"notifications"`
}

func (s *InboxService) preferencesFor(ctx context.Context, userID int) (notificationPreferenceDocument, bool, error) {
	var preference uModel.UserPreference
	err := s.db.WithContext(ctx).Where("user_id = ?", userID).First(&preference).Error

	// Both missing rows and legacy documents start from the canonical Settings
	// defaults. A user must never be implicitly opted into a newer category or
	// channel merely because they have not saved Settings yet.
	document := notificationPreferenceDocument{}
	d := preferences.DefaultNotificationSettings()
	document.Notifications.TaskReminders, document.Notifications.RoutineNudges = d.TaskReminders, d.RoutineNudges
	document.Notifications.FamilyActivity, document.Notifications.Rewards = d.FamilyActivity, d.Rewards
	document.Notifications.WeeklySummary, document.Notifications.QuietHours = d.WeeklySummary, d.QuietHours
	document.Notifications.PushChannel, document.Notifications.EmailChannel = d.PushChannel, d.EmailChannel

	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && preference.Value == "") {
		return document, true, nil
	}
	if err != nil {
		return notificationPreferenceDocument{}, false, err
	}
	if err := json.Unmarshal([]byte(preference.Value), &document); err != nil {
		return notificationPreferenceDocument{}, false, err
	}
	return document, true, nil
}

func (s *InboxService) quietHoursAllow(ctx context.Context, userID int, enabled bool, at time.Time) (bool, error) {
	if !enabled {
		return true, nil
	}
	var user uModel.User
	if err := s.db.WithContext(ctx).Select("timezone").First(&user, userID).Error; err != nil {
		return false, err
	}
	location, err := time.LoadLocation(user.Timezone)
	if err != nil {
		location = time.UTC
	}
	hour := at.In(location).Hour()
	return hour < 22 && hour >= 8, nil
}

func notificationPreferenceEnabled(p notificationPreferenceDocument, kind string) bool {
	switch kind {
	case "routine":
		return p.Notifications.RoutineNudges
	case "family":
		return p.Notifications.FamilyActivity
	case "reward":
		return p.Notifications.Rewards
	case "weekly":
		return p.Notifications.WeeklySummary
	default:
		return p.Notifications.TaskReminders
	}
}

func notificationKind(typeName string) string {
	switch {
	case len(typeName) >= len("routine_") && typeName[:len("routine_")] == "routine_":
		return "routine"
	case len(typeName) >= len("family_") && typeName[:len("family_")] == "family_":
		return "family"
	case len(typeName) >= len("reward_") && typeName[:len("reward_")] == "reward_":
		return "reward"
	case len(typeName) >= len("weekly_") && typeName[:len("weekly_")] == "weekly_":
		return "weekly"
	default:
		return "task"
	}
}

func (s *InboxService) notificationAllowed(ctx context.Context, n *nModel.InboxNotification) (bool, error) {
	document, exists, err := s.preferencesFor(ctx, n.UserID)
	if err != nil {
		return false, err
	}
	if !exists {
		return true, nil
	}
	if !notificationPreferenceEnabled(document, notificationKind(n.Type)) {
		return false, nil
	}
	return s.quietHoursAllow(ctx, n.UserID, document.Notifications.QuietHours, n.CreatedAt)
}

// AllowsExternalTaskDelivery is the scheduler-side equivalent of inbox
// filtering. Preferences can change after a task reminder has been queued, so
// the final gate belongs immediately before sending—not only at generation.
func (s *InboxService) AllowsExternalTaskDelivery(ctx context.Context, userID int, platform nModel.NotificationPlatform, at time.Time) (bool, error) {
	document, exists, err := s.preferencesFor(ctx, userID)
	if err != nil || !exists {
		return err == nil, err
	}
	if !document.Notifications.TaskReminders {
		return false, nil
	}
	// FCM is the app's push delivery path. The remaining configured platforms
	// are explicit third-party targets and have no corresponding email channel.
	if platform == nModel.NotificationPlatformFCM && !document.Notifications.PushChannel {
		return false, nil
	}
	return s.quietHoursAllow(ctx, userID, document.Notifications.QuietHours, at)
}

// GenerateScheduled observes the same chores and meter schedules used by the
// existing scheduler and Meter Engine. Dedup keys include the authoritative due
// occurrence, so retries are safe while later recurrences remain notifyable.
func (s *InboxService) GenerateScheduled(ctx context.Context, now time.Time) error {
	now = now.UTC()
	routineChores, err := s.routineChoreIDs(ctx)
	if err != nil {
		return err
	}
	var chores []chModel.Chore
	if err := s.db.WithContext(ctx).Where("is_active = ? AND assigned_to IS NOT NULL AND next_due_date IS NOT NULL AND next_due_date <= ?", true, now).Preload("MeterSchedule").Find(&chores).Error; err != nil {
		return err
	}
	for i := range chores {
		ch := &chores[i]
		if routineChores[ch.ID] {
			continue
		}
		if ch.AssignedTo == nil || ch.NextDueDate == nil {
			continue
		}
		occ := ch.NextDueDate.UTC().Format(time.RFC3339Nano)
		typeName, title, message, severity := "task_due", ch.Name+" is due", "Due now", "warning"
		entityType := "chore"
		var reminder struct{ MeterID int }
		if s.db.WithContext(ctx).Table("meter_update_reminders").Select("meter_id").Where("chore_id = ?", ch.ID).Scan(&reminder).Error == nil && reminder.MeterID > 0 {
			var meter mModel.Meter
			if s.db.WithContext(ctx).Where("id = ?", reminder.MeterID).First(&meter).Error == nil {
				typeName = "meter_update_due"
				title = "Update " + meter.Name
				message = "Your scheduled " + string(meter.Unit) + " reading is due."
				entityType = "meter"
			}
		} else {
			var assetLinkCount int64
			_ = s.db.WithContext(ctx).Table("thing_chores").Where("chore_id = ?", ch.ID).Count(&assetLinkCount).Error
			if assetLinkCount > 0 {
				// Meter-linked maintenance is emitted below from the populated Meter
				// Engine state; calendar-only maintenance is authoritative here.
				if ch.MeterSchedule != nil {
					continue
				}
				typeName, severity, title, message = "maintenance_due", "danger", ch.Name+" is due", "Maintenance interval reached"
			}
		}
		if now.After(ch.NextDueDate.Add(24 * time.Hour)) {
			if typeName == "meter_update_due" {
				typeName = "meter_update_overdue"
				title += " reading is overdue"
			} else {
				typeName = "task_overdue"
				title = ch.Name + " is overdue"
			}
			message = "This needs attention"
			severity = "danger"
		}
		id := ch.ID
		if entityType == "meter" {
			id = reminder.MeterID
		}
		_ = s.Notify(ctx, &nModel.InboxNotification{CircleID: ch.CircleID, UserID: *ch.AssignedTo, Type: typeName, Severity: severity, Title: title, Message: message, EntityType: entityType, EntityID: &id, DedupKey: fmt.Sprintf("%s:%d:%d:%s", typeName, *ch.AssignedTo, id, occ), CreatedAt: now})
	}
	if err := s.generateRoutineNudges(ctx, now, routineChores); err != nil {
		return err
	}
	if err := s.generateMaintenance(ctx, now); err != nil {
		return err
	}
	return s.GenerateWeeklySummary(ctx, now)
}

// routineChoreIDs follows the authoritative routine-application receipt; it
// never invents another schedule or guesses from a chore name.
func (s *InboxService) routineChoreIDs(ctx context.Context) (map[int]bool, error) {
	if !s.db.Migrator().HasTable("routine_applications") {
		return map[int]bool{}, nil
	}
	var applications []routineApplicationRecord
	if err := s.db.WithContext(ctx).Find(&applications).Error; err != nil {
		return nil, err
	}
	ids := make(map[int]bool)
	for _, application := range applications {
		for _, id := range application.ChoreIDs {
			ids[id] = true
		}
	}
	return ids, nil
}

func (s *InboxService) generateRoutineNudges(ctx context.Context, now time.Time, routineChores map[int]bool) error {
	if len(routineChores) == 0 {
		return nil
	}
	if !s.db.Migrator().HasTable("routine_packs") {
		return nil
	}
	var packs []routinePackRecord
	if err := s.db.WithContext(ctx).Where("is_active = ?", true).Find(&packs).Error; err != nil {
		return err
	}
	activePacks := make(map[int]bool)
	for _, pack := range packs {
		activePacks[pack.ID] = true
	}
	var applications []routineApplicationRecord
	if err := s.db.WithContext(ctx).Find(&applications).Error; err != nil {
		return err
	}
	activeRoutineChores := make(map[int]bool)
	for _, application := range applications {
		if activePacks[application.PackID] {
			for _, id := range application.ChoreIDs {
				activeRoutineChores[id] = true
			}
		}
	}
	var chores []chModel.Chore
	if err := s.db.WithContext(ctx).Where("is_active = ? AND assigned_to IS NOT NULL AND next_due_date IS NOT NULL AND next_due_date <= ?", true, now).Find(&chores).Error; err != nil {
		return err
	}
	for _, chore := range chores {
		if !routineChores[chore.ID] || !activeRoutineChores[chore.ID] || chore.AssignedTo == nil || chore.NextDueDate == nil {
			continue
		}
		id, occurrence := chore.ID, chore.NextDueDate.UTC().Format(time.RFC3339Nano)
		_ = s.Notify(ctx, &nModel.InboxNotification{CircleID: chore.CircleID, UserID: *chore.AssignedTo, Type: "routine_due", Severity: "warning", Title: chore.Name + " is due", Message: "Your routine is due now", EntityType: "chore", EntityID: &id, DedupKey: fmt.Sprintf("routine_due:%d:%d:%s", chore.ID, *chore.AssignedTo, occurrence), CreatedAt: now})
	}
	return nil
}

// NotifyFamilyActivity is called only after the authoritative completion write
// succeeds. The history row supplies both the actual actor and the durable
// occurrence identity, so approval retry cannot be attributed to its approver
// or create duplicate recipient notifications.
func (s *InboxService) NotifyFamilyActivity(ctx context.Context, chore *chModel.Chore, completion *chModel.ChoreHistory) error {
	if completion == nil || completion.ID == 0 || completion.PerformedAt == nil || completion.Status != chModel.ChoreHistoryStatusCompleted {
		return nil
	}
	visible, err := preferences.ActivityVisible(ctx, s.db, chore.CircleID, completion.CompletedBy)
	if err != nil || !visible {
		return err
	}
	var members []cModel.UserCircle
	if err := s.db.WithContext(ctx).Where("circle_id = ? AND is_active = ?", chore.CircleID, true).Find(&members).Error; err != nil {
		return err
	}
	for _, member := range members {
		if member.UserID == completion.CompletedBy {
			continue
		}
		id, title, message := chore.ID, "A team member completed a task", chore.Name
		if chore.IsPrivate && member.UserID != chore.CreatedBy && (chore.AssignedTo == nil || member.UserID != *chore.AssignedTo) {
			message = "A private task was completed"
		}
		_ = s.Notify(ctx, &nModel.InboxNotification{CircleID: chore.CircleID, UserID: member.UserID, Type: "family_activity_completed", Severity: "success", Title: title, Message: message, EntityType: "chore", EntityID: &id, DedupKey: fmt.Sprintf("family_activity:%d:%d:%d", completion.ID, chore.ID, member.UserID), CreatedAt: *completion.PerformedAt})
	}
	return nil
}

// GenerateWeeklySummary creates the Friday digest from the recipient's own
// completed-work ledger. It never queries a circle aggregate, so a digest
// cannot disclose another member's activity. The inbox unique dedup key makes
// it safe across ticks, retries, and process restarts.
func (s *InboxService) GenerateWeeklySummary(ctx context.Context, now time.Time) error {
	var users []uModel.User
	if err := s.db.WithContext(ctx).Select("id", "circle_id", "timezone").Find(&users).Error; err != nil {
		return err
	}
	for _, user := range users {
		location, err := time.LoadLocation(user.Timezone)
		if err != nil {
			location = time.UTC
		}
		local := now.In(location)
		if local.Weekday() != time.Friday {
			continue
		}
		weekStart := time.Date(local.Year(), local.Month(), local.Day()-int(local.Weekday())+1, 0, 0, 0, 0, location)
		weekEnd := weekStart.AddDate(0, 0, 7)
		var completed int64
		if err := s.db.WithContext(ctx).Table("chore_histories h").Joins("JOIN chores ch ON ch.id = h.chore_id").
			Where("ch.circle_id = ? AND h.completed_by = ? AND h.status = ? AND h.performed_at >= ? AND h.performed_at < ?", user.CircleID, user.ID, chModel.ChoreHistoryStatusCompleted, weekStart.UTC(), weekEnd.UTC()).Count(&completed).Error; err != nil {
			return err
		}
		year, week := local.ISOWeek()
		message := fmt.Sprintf("You completed %d task", completed)
		if completed != 1 {
			message += "s"
		}
		message += " this week."
		_ = s.Notify(ctx, &nModel.InboxNotification{CircleID: user.CircleID, UserID: user.ID, Type: "weekly_summary", Severity: "info", Title: "Your weekly progress", Message: message, EntityType: "weekly_summary", DedupKey: fmt.Sprintf("weekly_summary:%d:%d:%d", user.ID, year, week), CreatedAt: now})
	}
	return nil
}

func (s *InboxService) generateMaintenance(ctx context.Context, now time.Time) error {
	var schedules []mModel.ChoreSchedule
	if err := s.db.WithContext(ctx).Find(&schedules).Error; err != nil {
		return err
	}
	for i := range schedules {
		sc := &schedules[i]
		var meter mModel.Meter
		var chore chModel.Chore
		if s.db.WithContext(ctx).Where("id = ?", sc.MeterID).First(&meter).Error != nil || s.db.WithContext(ctx).Where("id = ? AND is_active = ?", sc.ChoreID, true).First(&chore).Error != nil || chore.AssignedTo == nil {
			continue
		}
		mRepo.PopulateScheduleAt(sc, meter, now)
		if sc.Status != "approaching" && sc.Status != "due" {
			continue
		}
		typeName, severity, title, message := "maintenance_approaching", "warning", chore.Name+" is approaching", fmt.Sprintf("%d %s remaining", sc.Remaining, sc.Unit)
		state := "approaching"
		if sc.Status == "due" {
			typeName = "maintenance_due"
			severity = "danger"
			title = chore.Name + " is due"
			message = maintenanceReason(sc)
			state = "due:" + sc.DueReason
		}
		id := chore.ID
		// Baseline identifies a maintenance cycle without suppressing the next service interval.
		key := fmt.Sprintf("%s:%d:%d:%d:%s", typeName, *chore.AssignedTo, chore.ID, sc.Baseline, state)
		_ = s.Notify(ctx, &nModel.InboxNotification{CircleID: chore.CircleID, UserID: *chore.AssignedTo, Type: typeName, Severity: severity, Title: title, Message: message, EntityType: "chore", EntityID: &id, DedupKey: key, CreatedAt: now})
	}
	return nil
}
func maintenanceReason(s *mModel.ChoreSchedule) string {
	switch s.DueReason {
	case "both":
		return "Mileage and date thresholds reached"
	case "meter":
		return "Meter threshold reached"
	case "time":
		return "Maintenance interval reached"
	default:
		return "Maintenance is due"
	}
}
