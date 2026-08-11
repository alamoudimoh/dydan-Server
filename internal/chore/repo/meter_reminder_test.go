package chore

import (
	"context"
	"errors"
	"testing"
	"time"

	"donetick.com/core/config"
	chModel "donetick.com/core/internal/chore/model"
	cModel "donetick.com/core/internal/circle/model"
	mModel "donetick.com/core/internal/meter/model"
	mRepo "donetick.com/core/internal/meter/repo"
	syncModel "donetick.com/core/internal/sync/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestMeterReminderLifecyclePreservesHistoryAndIsIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&chModel.Chore{}, &chModel.ChoreAssignees{}, &chModel.ChoreHistory{}, &cModel.UserCircle{}, &mModel.Meter{}, &mModel.Reading{}, &mModel.UpdateReminder{}, &mModel.ChoreSchedule{}, &syncModel.SyncCursor{}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	now := time.Date(2026, time.August, 14, 15, 0, 0, 0, time.UTC)
	friday := time.Date(2026, time.August, 14, 15, 0, 0, 0, time.UTC)
	sunday := time.Date(2026, time.August, 16, 15, 0, 0, 0, time.UTC)
	if err = db.Create(&cModel.UserCircle{UserID: 1, CircleID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	meter := mModel.Meter{CircleID: 1, CreatedBy: 1, Name: "Odometer", Unit: mModel.UnitKilometers, CurrentReading: 80000, CreatedAt: now, UpdatedAt: now}
	if err = db.Create(&meter).Error; err != nil {
		t.Fatal(err)
	}
	for _, v := range []int64{80000, 82000, 84000} {
		if err = db.Create(&mModel.Reading{MeterID: meter.ID, Reading: v, Kind: mModel.ReadingKindReading, RecordedBy: 1, RecordedAt: now.Add(time.Duration(v) * time.Second)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	repo := NewChoreRepository(db, &config.Config{Database: config.DatabaseConfig{Type: "sqlite"}})
	fridaySpec := MeterReminderSpec{Enabled: true, FrequencyType: chModel.FrequencyTypeWeekly, Frequency: 1, NextDueDate: &friday, AssignedTo: 1}
	if err = repo.ConfigureMeterReminder(ctx, meter.ID, 1, 1, fridaySpec); err != nil {
		t.Fatal(err)
	}
	var reminder mModel.UpdateReminder
	if err = db.First(&reminder, "meter_id = ?", meter.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err = repo.ConfigureMeterReminder(ctx, meter.ID, 1, 1, MeterReminderSpec{Enabled: true, FrequencyType: chModel.FrequencyTypeWeekly, Frequency: 1, NextDueDate: &sunday, AssignedTo: 1}); err != nil {
		t.Fatal(err)
	}
	var reminders int64
	db.Model(&mModel.UpdateReminder{}).Where("meter_id = ?", meter.ID).Count(&reminders)
	if reminders != 1 {
		t.Fatalf("duplicate reminder relation: %d", reminders)
	}
	if err = repo.ConfigureMeterReminder(ctx, meter.ID, 1, 1, MeterReminderSpec{Enabled: false}); err != nil {
		t.Fatal(err)
	}
	if err = repo.ConfigureMeterReminder(ctx, meter.ID, 1, 1, fridaySpec); err != nil {
		t.Fatal(err)
	}
	next := friday.AddDate(0, 0, 7)
	if err = repo.CompleteMeterReminder(ctx, meter.ID, 1, 1, 86000, &friday, &next); err != nil {
		t.Fatal(err)
	}
	if err = repo.CompleteMeterReminder(ctx, meter.ID, 1, 1, 86000, &friday, &next); !errors.Is(err, ErrMeterReminderAlreadyCompleted) {
		t.Fatalf("retry was not idempotently rejected: %v", err)
	}
	var readings []mModel.Reading
	db.Where("meter_id = ?", meter.ID).Order("reading").Find(&readings)
	if len(readings) != 4 || readings[3].Reading != 86000 {
		t.Fatalf("append-only history failed: %#v", readings)
	}
	if err = repo.CompleteMeterReminder(ctx, meter.ID, 1, 1, 85000, &next, &next); !errors.Is(err, mRepo.ErrDecreasingReading) {
		t.Fatalf("decreasing reading accepted: %v", err)
	}
}
