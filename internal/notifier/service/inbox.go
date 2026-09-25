package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	mModel "github.com/alamoudimoh/dydan-server/internal/meter/model"
	mRepo "github.com/alamoudimoh/dydan-server/internal/meter/repo"
	nModel "github.com/alamoudimoh/dydan-server/internal/notifier/model"
	nRepo "github.com/alamoudimoh/dydan-server/internal/notifier/repo"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"gorm.io/gorm"
)

type InboxService struct {
	db   *gorm.DB
	repo *nRepo.InboxRepository
}

func NewInboxService(db *gorm.DB, repo *nRepo.InboxRepository) *InboxService {
	return &InboxService{db: db, repo: repo}
}

func (s *InboxService) Notify(ctx context.Context, n *nModel.InboxNotification) error {
	allowed, err := s.notificationAllowed(ctx, n)
	if err != nil {
		return err
	}
	if !allowed {
		return nil
	}
	_, err = s.repo.Create(ctx, n)
	return err
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
	if errors.Is(err, gorm.ErrRecordNotFound) || preference.Value == "" {
		return notificationPreferenceDocument{}, false, nil
	}
	if err != nil {
		return notificationPreferenceDocument{}, false, err
	}
	// Missing keys come from exports created before notification categories and
	// must retain the historical enabled behavior after an upgrade.
	document := notificationPreferenceDocument{}
	document.Notifications.TaskReminders = true
	document.Notifications.RoutineNudges = true
	document.Notifications.FamilyActivity = true
	document.Notifications.Rewards = true
	document.Notifications.WeeklySummary = true
	document.Notifications.PushChannel = true
	document.Notifications.EmailChannel = true
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
	var chores []chModel.Chore
	if err := s.db.WithContext(ctx).Where("is_active = ? AND assigned_to IS NOT NULL AND next_due_date IS NOT NULL AND next_due_date <= ?", true, now).Preload("MeterSchedule").Find(&chores).Error; err != nil {
		return err
	}
	for i := range chores {
		ch := &chores[i]
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
	return s.generateMaintenance(ctx, now)
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
