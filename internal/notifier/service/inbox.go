package service

import (
	"context"
	"fmt"
	"time"

	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	mModel "github.com/alamoudimoh/dydan-server/internal/meter/model"
	mRepo "github.com/alamoudimoh/dydan-server/internal/meter/repo"
	nModel "github.com/alamoudimoh/dydan-server/internal/notifier/model"
	nRepo "github.com/alamoudimoh/dydan-server/internal/notifier/repo"
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
	_, err := s.repo.Create(ctx, n)
	return err
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
