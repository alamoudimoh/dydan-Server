package repo

import (
	"context"
	"errors"
	"time"

	mModel "donetick.com/core/internal/meter/model"
	syncModel "donetick.com/core/internal/sync/model"
	"gorm.io/gorm"
)

var ErrDecreasingReading = errors.New("meter reading cannot decrease without a correction")

type Repository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

func (r *Repository) Create(ctx context.Context, meter *mModel.Meter, actorID int) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(meter).Error; err != nil {
			return err
		}
		return tx.Create(&mModel.Reading{MeterID: meter.ID, Reading: meter.CurrentReading, Kind: mModel.ReadingKindReading, RecordedBy: actorID, RecordedAt: meter.CreatedAt}).Error
	})
}

func (r *Repository) Get(ctx context.Context, id, circleID int) (*mModel.Meter, error) {
	var meter mModel.Meter
	err := r.db.WithContext(ctx).Where("id = ? AND circle_id = ?", id, circleID).First(&meter).Error
	return &meter, err
}

func (r *Repository) List(ctx context.Context, circleID int) ([]mModel.Meter, error) {
	var meters []mModel.Meter
	err := r.db.WithContext(ctx).Where("circle_id = ?", circleID).Order("name").Find(&meters).Error
	return meters, err
}

func (r *Repository) UpdateMetadata(ctx context.Context, id, circleID int, name string, unit mModel.Unit) error {
	result := r.db.WithContext(ctx).Model(&mModel.Meter{}).Where("id = ? AND circle_id = ?", id, circleID).Updates(map[string]interface{}{"name": name, "unit": unit})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return gorm.ErrRecordNotFound
	}
	return nil
}

func (r *Repository) AddReading(ctx context.Context, id, circleID int, value int64, actorID int, correction bool, note *string) (*mModel.Meter, error) {
	var meter mModel.Meter
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("id = ? AND circle_id = ?", id, circleID).First(&meter).Error; err != nil {
			return err
		}
		if value == meter.CurrentReading && !correction {
			return nil
		}
		if value < meter.CurrentReading && !correction {
			return ErrDecreasingReading
		}
		previous := meter.CurrentReading
		kind := mModel.ReadingKindReading
		if correction {
			kind = mModel.ReadingKindCorrection
		}
		now := time.Now().UTC()
		if err := tx.Model(&meter).Updates(map[string]interface{}{"current_reading": value, "updated_at": now}).Error; err != nil {
			return err
		}
		return tx.Create(&mModel.Reading{MeterID: id, Reading: value, PreviousReading: &previous, Kind: kind, Note: note, RecordedBy: actorID, RecordedAt: now}).Error
	})
	if err == nil {
		meter.CurrentReading = value
	}
	return &meter, err
}

func (r *Repository) History(ctx context.Context, id, circleID, offset int) ([]mModel.Reading, error) {
	if _, err := r.Get(ctx, id, circleID); err != nil {
		return nil, err
	}
	var readings []mModel.Reading
	err := r.db.WithContext(ctx).Where("meter_id = ?", id).Order("recorded_at DESC, id DESC").Offset(offset).Limit(20).Find(&readings).Error
	return readings, err
}

func (r *Repository) UpsertSchedule(ctx context.Context, choreID, circleID int, req mModel.ChoreScheduleRequest) error {
	meter, err := r.Get(ctx, req.MeterID, circleID)
	if err != nil {
		return err
	}
	baseline := meter.CurrentReading
	var chore struct{ NextDueDate *time.Time }
	if err := r.db.WithContext(ctx).Table("chores").Select("next_due_date").Where("id = ? AND circle_id = ?", choreID, circleID).Scan(&chore).Error; err != nil {
		return err
	}
	var existing mModel.ChoreSchedule
	if err := r.db.WithContext(ctx).Where("chore_id = ?", choreID).First(&existing).Error; err == nil && existing.MeterID == meter.ID {
		baseline = existing.Baseline
	}
	s := mModel.ChoreSchedule{ChoreID: choreID, MeterID: meter.ID, Interval: req.Interval, Baseline: baseline, CalendarDueDate: chore.NextDueDate}
	return r.db.WithContext(ctx).Save(&s).Error
}

func (r *Repository) DeleteSchedule(ctx context.Context, choreID int) error {
	return r.db.WithContext(ctx).Where("chore_id = ?", choreID).Delete(&mModel.ChoreSchedule{}).Error
}

func (r *Repository) MarkLinkedChoresDue(ctx context.Context, meterID, circleID int) error {
	meter, err := r.Get(ctx, meterID, circleID)
	if err != nil {
		return err
	}
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var choreIDs []int
		if err := tx.Table("chores").
			Select("chores.id").
			Joins("JOIN chore_meter_schedules cms ON cms.chore_id = chores.id").
			Where("chores.circle_id = ? AND chores.is_active = ? AND cms.meter_id = ? AND cms.baseline + cms.interval <= ? AND (chores.next_due_date IS NULL OR chores.next_due_date > ?)", circleID, true, meterID, meter.CurrentReading, now).
			Scan(&choreIDs).Error; err != nil {
			return err
		}
		for _, choreID := range choreIDs {
			var version int64
			if err := tx.Raw(`INSERT INTO sync_cursors (circle_id, entity_type, max_version) VALUES (?, ?, 1) ON CONFLICT (circle_id, entity_type) DO UPDATE SET max_version = sync_cursors.max_version + 1 RETURNING max_version`, circleID, syncModel.EntityTypeChore).Scan(&version).Error; err != nil {
				return err
			}
			if err := tx.Table("chores").Where("id = ? AND (next_due_date IS NULL OR next_due_date > ?)", choreID, now).Updates(map[string]interface{}{"next_due_date": now, "updated_at": now, "sync_version": version}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func PopulateSchedule(schedule *mModel.ChoreSchedule, meter mModel.Meter) {
	schedule.CurrentReading = meter.CurrentReading
	schedule.Target = schedule.Baseline + schedule.Interval
	schedule.Unit = meter.Unit
	schedule.MeterName = meter.Name
	if schedule.Interval <= 0 {
		return
	}
	schedule.Used = meter.CurrentReading - schedule.Baseline
	if schedule.Used < 0 {
		schedule.Used = 0
	}
	schedule.Remaining = schedule.Target - meter.CurrentReading
	if schedule.Remaining < 0 {
		schedule.Remaining = 0
	}
	schedule.Progress = int(schedule.Used * 100 / schedule.Interval)
	if schedule.Progress > 100 {
		schedule.Progress = 100
	}
	schedule.DueByMeter = meter.CurrentReading >= schedule.Target
	schedule.DueByTime = schedule.CalendarDueDate != nil && !schedule.CalendarDueDate.After(time.Now().UTC())
	switch {
	case schedule.DueByMeter && schedule.DueByTime:
		schedule.DueReason = "both"
	case schedule.DueByMeter:
		schedule.DueReason = "meter"
	case schedule.DueByTime:
		schedule.DueReason = "time"
	}
}
