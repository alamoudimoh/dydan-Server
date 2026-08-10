package repo

import (
	"context"
	"errors"
	"testing"
	"time"

	chModel "donetick.com/core/internal/chore/model"
	mModel "donetick.com/core/internal/meter/model"
	syncModel "donetick.com/core/internal/sync/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func testRepository(t *testing.T) (*Repository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&mModel.Meter{}, &mModel.Reading{}, &mModel.ChoreSchedule{}, &chModel.Chore{}, &syncModel.SyncCursor{}); err != nil {
		t.Fatal(err)
	}
	return NewRepository(db), db
}

func TestMeterReadingHistoryMonotonicAndCorrection(t *testing.T) {
	repo, _ := testRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	meter := &mModel.Meter{CircleID: 1, CreatedBy: 7, Name: "Vehicle", Unit: mModel.UnitKilometers, CurrentReading: 80000, CreatedAt: now, UpdatedAt: now}
	if err := repo.Create(ctx, meter, 7); err != nil {
		t.Fatal(err)
	}
	updated, err := repo.AddReading(ctx, meter.ID, 1, 82000, 8, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.CurrentReading != 82000 {
		t.Fatalf("updated meter returned stale reading: %d", updated.CurrentReading)
	}
	if _, err := repo.AddReading(ctx, meter.ID, 1, 81000, 8, false, nil); !errors.Is(err, ErrDecreasingReading) {
		t.Fatalf("expected decreasing error, got %v", err)
	}
	note := "odometer entry typo"
	if _, err := repo.AddReading(ctx, meter.ID, 1, 81000, 8, true, &note); err != nil {
		t.Fatal(err)
	}
	history, err := repo.History(ctx, meter.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 3 {
		t.Fatalf("expected initial, update and correction; got %d", len(history))
	}
	if history[0].Kind != mModel.ReadingKindCorrection || history[0].PreviousReading == nil || *history[0].PreviousReading != 82000 {
		t.Fatalf("correction audit fields missing: %#v", history[0])
	}
	if _, err := repo.Get(ctx, meter.ID, 2); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-circle read should fail, got %v", err)
	}
}

func TestMeterThresholdMovesDueDateOnlyOnce(t *testing.T) {
	repo, db := testRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	future := now.Add(180 * 24 * time.Hour)
	meter := &mModel.Meter{CircleID: 1, CreatedBy: 1, Name: "Odometer", Unit: mModel.UnitKilometers, CurrentReading: 80000, CreatedAt: now, UpdatedAt: now}
	if err := repo.Create(ctx, meter, 1); err != nil {
		t.Fatal(err)
	}
	chore := &chModel.Chore{Name: "Oil", CircleID: 1, FrequencyType: chModel.FrequencyTypeInterval, Frequency: 6, NextDueDate: &future, IsActive: true, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(chore).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertSchedule(ctx, chore.ID, 1, mModel.ChoreScheduleRequest{MeterID: meter.ID, Interval: 10000}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AddReading(ctx, meter.ID, 1, 90000, 1, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkLinkedChoresDue(ctx, meter.ID, 1); err != nil {
		t.Fatal(err)
	}
	var first chModel.Chore
	db.First(&first, chore.ID)
	if first.NextDueDate == nil || first.NextDueDate.After(time.Now().UTC().Add(time.Second)) {
		t.Fatalf("meter threshold did not make chore due: %v", first.NextDueDate)
	}
	due := *first.NextDueDate
	var savedSchedule mModel.ChoreSchedule
	db.First(&savedSchedule, "chore_id = ?", chore.ID)
	refreshedMeter, err := repo.Get(ctx, meter.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	PopulateSchedule(&savedSchedule, *refreshedMeter)
	if savedSchedule.DueReason != "meter" || savedSchedule.DueByTime {
		t.Fatalf("hybrid chore due reason is incorrect: %#v", savedSchedule)
	}
	if _, err := repo.AddReading(ctx, meter.ID, 1, 91000, 1, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkLinkedChoresDue(ctx, meter.ID, 1); err != nil {
		t.Fatal(err)
	}
	var second chModel.Chore
	db.First(&second, chore.ID)
	if !second.NextDueDate.Equal(due) {
		t.Fatalf("repeated reading changed already-due date")
	}
}
