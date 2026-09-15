package chore

import (
	"context"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	mModel "github.com/alamoudimoh/dydan-server/internal/meter/model"
	syncModel "github.com/alamoudimoh/dydan-server/internal/sync/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestCompleteChoreRebasesFromActualReadingAndKeepsMeterOnlyActive(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&chModel.Chore{}, &chModel.ChoreHistory{}, &chModel.TimeSession{}, &cModel.UserCircle{}, &mModel.Meter{}, &mModel.ChoreSchedule{}, &syncModel.SyncCursor{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	meter := mModel.Meter{CircleID: 1, CreatedBy: 1, Name: "Vehicle", Unit: mModel.UnitKilometers, CurrentReading: 90500, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&meter).Error; err != nil {
		t.Fatal(err)
	}
	chore := chModel.Chore{Name: "Oil service", CircleID: 1, CreatedBy: 1, UpdatedBy: 1, FrequencyType: chModel.FrequencyTypeNoRepeat, Frequency: 1, IsActive: true, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&chore).Error; err != nil {
		t.Fatal(err)
	}
	schedule := mModel.ChoreSchedule{ChoreID: chore.ID, MeterID: meter.ID, Interval: 10000, Baseline: 80000}
	if err := db.Create(&schedule).Error; err != nil {
		t.Fatal(err)
	}
	repo := NewChoreRepository(db, &config.Config{Database: config.DatabaseConfig{Type: "sqlite"}})
	if err := repo.CompleteChore(context.Background(), &chore, nil, 1, nil, &now, nil, false, nil); err != nil {
		t.Fatal(err)
	}
	var savedSchedule mModel.ChoreSchedule
	db.First(&savedSchedule, "chore_id = ?", chore.ID)
	if savedSchedule.Baseline != 90500 || savedSchedule.Baseline+savedSchedule.Interval != 100500 {
		t.Fatalf("next threshold not recalculated from completion: %#v", savedSchedule)
	}
	var history chModel.ChoreHistory
	db.Where("chore_id = ?", chore.ID).First(&history)
	if history.MeterID == nil || history.MeterReading == nil || *history.MeterReading != 90500 {
		t.Fatalf("completion meter snapshot missing: %#v", history)
	}
	var savedChore chModel.Chore
	db.First(&savedChore, chore.ID)
	if !savedChore.IsActive {
		t.Fatal("meter-only chore was archived after completion")
	}
}

func TestCompleteHybridChoreResetsTimeAndMeterWithoutResettingMeter(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&chModel.Chore{}, &chModel.ChoreHistory{}, &chModel.TimeSession{}, &cModel.UserCircle{}, &mModel.Meter{}, &mModel.ChoreSchedule{}, &syncModel.SyncCursor{}); err != nil {
		t.Fatal(err)
	}
	completedAt := time.Date(2026, time.January, 31, 9, 30, 0, 0, time.UTC)
	nextCalendarDue := completedAt.AddDate(0, 6, 0)
	meter := mModel.Meter{CircleID: 1, CreatedBy: 1, Name: "Vehicle", Unit: mModel.UnitKilometers, CurrentReading: 93250, CreatedAt: completedAt, UpdatedAt: completedAt}
	if err := db.Create(&meter).Error; err != nil {
		t.Fatal(err)
	}
	chore := chModel.Chore{Name: "Hybrid oil service", CircleID: 1, CreatedBy: 1, UpdatedBy: 1, FrequencyType: chModel.FrequencyTypeMonthly, Frequency: 6, IsActive: true, CreatedAt: completedAt, UpdatedAt: completedAt}
	if err := db.Create(&chore).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&mModel.ChoreSchedule{ChoreID: chore.ID, MeterID: meter.ID, Interval: 10000, Baseline: 83250}).Error; err != nil {
		t.Fatal(err)
	}
	repository := NewChoreRepository(db, &config.Config{Database: config.DatabaseConfig{Type: "sqlite"}})
	if err := repository.CompleteChore(context.Background(), &chore, nil, 1, &nextCalendarDue, &completedAt, nil, false, nil); err != nil {
		t.Fatal(err)
	}
	var savedMeter mModel.Meter
	if err := db.First(&savedMeter, meter.ID).Error; err != nil {
		t.Fatal(err)
	}
	var savedSchedule mModel.ChoreSchedule
	if err := db.First(&savedSchedule, "chore_id = ?", chore.ID).Error; err != nil {
		t.Fatal(err)
	}
	var savedChore chModel.Chore
	if err := db.First(&savedChore, chore.ID).Error; err != nil {
		t.Fatal(err)
	}
	if savedMeter.CurrentReading != 93250 {
		t.Fatalf("completion reset odometer: %d", savedMeter.CurrentReading)
	}
	if savedSchedule.Baseline != 93250 || savedSchedule.Baseline+savedSchedule.Interval != 103250 {
		t.Fatalf("meter side did not reset from completion reading: %#v", savedSchedule)
	}
	if savedSchedule.CalendarDueDate == nil || !savedSchedule.CalendarDueDate.Equal(nextCalendarDue) || savedChore.NextDueDate == nil || !savedChore.NextDueDate.Equal(nextCalendarDue) {
		t.Fatalf("time side did not reset: schedule=%v chore=%v want=%v", savedSchedule.CalendarDueDate, savedChore.NextDueDate, nextCalendarDue)
	}
}
