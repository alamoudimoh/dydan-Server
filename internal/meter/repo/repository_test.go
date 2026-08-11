package repo

import (
	"context"
	"errors"
	"path/filepath"
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

func TestMeterEngineDeterministicThresholdsAndHybridReasons(t *testing.T) {
	repo, db := testRepository(t)
	ctx := context.Background()
	evaluatedAt := time.Date(2026, time.January, 15, 12, 0, 0, 0, time.UTC)
	calendarDue := evaluatedAt.AddDate(0, 6, 0)
	meter := &mModel.Meter{CircleID: 1, CreatedBy: 1, Name: "Vehicle Odometer", Unit: mModel.UnitKilometers, CurrentReading: 80000, CreatedAt: evaluatedAt, UpdatedAt: evaluatedAt}
	if err := repo.Create(ctx, meter, 1); err != nil {
		t.Fatal(err)
	}
	chore := &chModel.Chore{Name: "Oil Change", CircleID: 1, FrequencyType: chModel.FrequencyTypeMonthly, Frequency: 6, NextDueDate: &calendarDue, IsActive: true, CreatedAt: evaluatedAt, UpdatedAt: evaluatedAt}
	if err := db.Create(chore).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertSchedule(ctx, chore.ID, 1, mModel.ChoreScheduleRequest{MeterID: meter.ID, Interval: 10000}); err != nil {
		t.Fatal(err)
	}

	assertState := func(reading int64, at time.Time, meterDue, timeDue bool, reason string) {
		t.Helper()
		if _, err := repo.AddReading(ctx, meter.ID, 1, reading, 1, false, nil); err != nil {
			t.Fatal(err)
		}
		var schedule mModel.ChoreSchedule
		if err := db.First(&schedule, "chore_id = ?", chore.ID).Error; err != nil {
			t.Fatal(err)
		}
		current, err := repo.Get(ctx, meter.ID, 1)
		if err != nil {
			t.Fatal(err)
		}
		PopulateScheduleAt(&schedule, *current, at)
		if schedule.Target != 90000 || schedule.DueByMeter != meterDue || schedule.DueByTime != timeDue || schedule.DueReason != reason {
			t.Fatalf("reading=%d at=%s: target=%d meterDue=%v timeDue=%v reason=%q", reading, at, schedule.Target, schedule.DueByMeter, schedule.DueByTime, schedule.DueReason)
		}
	}

	assertState(80000, evaluatedAt, false, false, "")
	assertState(89999, calendarDue.Add(-time.Second), false, false, "")
	assertState(90000, calendarDue.Add(-time.Second), true, false, "meter")
	assertState(90001, calendarDue, true, true, "both")

	// Time-first is evaluated independently with a below-threshold reading.
	var schedule mModel.ChoreSchedule
	db.First(&schedule, "chore_id = ?", chore.ID)
	meter.CurrentReading = 89999
	PopulateScheduleAt(&schedule, *meter, calendarDue)
	if schedule.DueReason != "time" || schedule.DueByMeter || !schedule.DueByTime {
		t.Fatalf("time-first evaluation incorrect: %#v", schedule)
	}
	PopulateScheduleAt(&schedule, *meter, evaluatedAt)
	if schedule.DueReason != "" || schedule.DueByMeter || schedule.DueByTime {
		t.Fatalf("not-due reevaluation retained stale due state: %#v", schedule)
	}
}

func TestApproachingWarningExactBoundaries(t *testing.T) {
	now := time.Date(2026, time.August, 1, 0, 0, 0, 0, time.UTC)
	meter := mModel.Meter{CurrentReading: 98999, Unit: mModel.UnitKilometers}
	schedule := mModel.ChoreSchedule{Baseline: 90000, Interval: 10000, WarnBefore: 1000}
	PopulateScheduleAt(&schedule, meter, now)
	if schedule.Status != "upcoming" || schedule.ApproachingByMeter {
		t.Fatalf("one outside warning should be upcoming: %#v", schedule)
	}
	meter.CurrentReading = 99000
	PopulateScheduleAt(&schedule, meter, now)
	if schedule.Status != "approaching" || schedule.Remaining != 1000 {
		t.Fatalf("exact warning boundary: %#v", schedule)
	}
	meter.CurrentReading = 99999
	PopulateScheduleAt(&schedule, meter, now)
	if schedule.Status != "approaching" || schedule.Remaining != 1 {
		t.Fatalf("one before target: %#v", schedule)
	}
	meter.CurrentReading = 100000
	PopulateScheduleAt(&schedule, meter, now)
	if schedule.Status != "due" || schedule.DueReason != "meter" {
		t.Fatalf("exact target: %#v", schedule)
	}
}

func TestMeterEngineHistoryRebaseAndNoRepeatedSideEffects(t *testing.T) {
	repo, db := testRepository(t)
	ctx := context.Background()
	now := time.Date(2026, time.March, 1, 8, 0, 0, 0, time.UTC)
	future := now.AddDate(0, 6, 0)
	meter := &mModel.Meter{CircleID: 1, CreatedBy: 1, Name: "Vehicle Odometer", Unit: mModel.UnitKilometers, CurrentReading: 80000, CreatedAt: now, UpdatedAt: now}
	if err := repo.Create(ctx, meter, 1); err != nil {
		t.Fatal(err)
	}
	chore := &chModel.Chore{Name: "Oil Change", CircleID: 1, FrequencyType: chModel.FrequencyTypeMonthly, Frequency: 6, NextDueDate: &future, IsActive: true, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(chore).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertSchedule(ctx, chore.ID, 1, mModel.ChoreScheduleRequest{MeterID: meter.ID, Interval: 10000}); err != nil {
		t.Fatal(err)
	}
	for _, reading := range []int64{85000, 89999, 90000} {
		if _, err := repo.AddReading(ctx, meter.ID, 1, reading, 1, false, nil); err != nil {
			t.Fatal(err)
		}
	}
	if err := repo.MarkLinkedChoresDueAt(ctx, meter.ID, 1, now); err != nil {
		t.Fatal(err)
	}
	var cursor syncModel.SyncCursor
	if err := db.Where("circle_id = ? AND entity_type = ?", 1, syncModel.EntityTypeChore).First(&cursor).Error; err != nil {
		t.Fatal(err)
	}
	version := cursor.MaxVersion
	if _, err := repo.AddReading(ctx, meter.ID, 1, 90001, 1, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.MarkLinkedChoresDueAt(ctx, meter.ID, 1, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	db.Where("circle_id = ? AND entity_type = ?", 1, syncModel.EntityTypeChore).First(&cursor)
	if cursor.MaxVersion != version {
		t.Fatalf("due side effect repeated: sync version %d -> %d", version, cursor.MaxVersion)
	}

	history, err := repo.History(ctx, meter.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(history) != 5 {
		t.Fatalf("expected five preserved readings, got %d", len(history))
	}
	for _, item := range history {
		if item.RecordedAt.IsZero() {
			t.Fatalf("history timestamp missing: %#v", item)
		}
	}
	if _, err := repo.AddReading(ctx, meter.ID, 1, 89000, 1, false, nil); !errors.Is(err, ErrDecreasingReading) {
		t.Fatalf("decreasing reading: %v", err)
	}
	current, _ := repo.Get(ctx, meter.ID, 1)
	historyAfter, _ := repo.History(ctx, meter.ID, 1, 0)
	if current.CurrentReading != 90001 || len(historyAfter) != len(history) {
		t.Fatalf("rejected reading corrupted state: current=%d history=%d", current.CurrentReading, len(historyAfter))
	}

	// Simulate completion at the actual 90,500 reading by rebasing the per-task schedule.
	if _, err := repo.AddReading(ctx, meter.ID, 1, 90500, 1, false, nil); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&mModel.ChoreSchedule{}).Where("chore_id = ?", chore.ID).Update("baseline", 90500).Error; err != nil {
		t.Fatal(err)
	}
	var schedule mModel.ChoreSchedule
	db.First(&schedule, "chore_id = ?", chore.ID)
	current.CurrentReading = 100499
	PopulateScheduleAt(&schedule, *current, now)
	if schedule.Target != 100500 || schedule.DueByMeter {
		t.Fatalf("completion rebase below boundary incorrect: %#v", schedule)
	}
	current.CurrentReading = 100500
	PopulateScheduleAt(&schedule, *current, now)
	if !schedule.DueByMeter {
		t.Fatalf("completion rebase boundary not due: %#v", schedule)
	}
}

func TestMultipleTasksShareMeterIndependently(t *testing.T) {
	repo, db := testRepository(t)
	ctx := context.Background()
	now := time.Date(2026, time.April, 10, 0, 0, 0, 0, time.UTC)
	meter := &mModel.Meter{CircleID: 1, CreatedBy: 1, Name: "Vehicle", Unit: mModel.UnitKilometers, CurrentReading: 90000, CreatedAt: now, UpdatedAt: now}
	if err := repo.Create(ctx, meter, 1); err != nil {
		t.Fatal(err)
	}
	type tc struct {
		name               string
		interval, baseline int64
		due                bool
	}
	cases := []tc{{"Oil", 10000, 80000, true}, {"Tires", 10000, 85000, false}, {"Transmission", 60000, 40000, false}}
	ids := make([]int, 0, len(cases))
	for _, item := range cases {
		future := now.AddDate(1, 0, 0)
		chore := &chModel.Chore{Name: item.name, CircleID: 1, NextDueDate: &future, IsActive: true, CreatedAt: now, UpdatedAt: now}
		if err := db.Create(chore).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&mModel.ChoreSchedule{ChoreID: chore.ID, MeterID: meter.ID, Interval: item.interval, Baseline: item.baseline, CalendarDueDate: &future}).Error; err != nil {
			t.Fatal(err)
		}
		ids = append(ids, chore.ID)
	}
	if err := repo.MarkLinkedChoresDueAt(ctx, meter.ID, 1, now); err != nil {
		t.Fatal(err)
	}
	for i, id := range ids {
		var chore chModel.Chore
		db.First(&chore, id)
		actualDue := chore.NextDueDate != nil && !chore.NextDueDate.After(now)
		if actualDue != cases[i].due {
			t.Fatalf("%s due=%v, want %v", cases[i].name, actualDue, cases[i].due)
		}
	}
	if err := db.Model(&mModel.ChoreSchedule{}).Where("chore_id = ?", ids[0]).Update("baseline", 90000).Error; err != nil {
		t.Fatal(err)
	}
	var schedules []mModel.ChoreSchedule
	db.Order("chore_id").Find(&schedules)
	if schedules[1].Baseline != 85000 || schedules[2].Baseline != 40000 {
		t.Fatalf("one completion changed another baseline: %#v", schedules)
	}
	current, _ := repo.Get(ctx, meter.ID, 1)
	if current.CurrentReading != 90000 {
		t.Fatalf("task completion reset shared meter: %d", current.CurrentReading)
	}
}

func TestMeterCircleIsolationAllRepositoryOperations(t *testing.T) {
	repo, db := testRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	meter := &mModel.Meter{CircleID: 1, CreatedBy: 1, Name: "Private Vehicle", Unit: mModel.UnitKilometers, CurrentReading: 10, CreatedAt: now, UpdatedAt: now}
	if err := repo.Create(ctx, meter, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Get(ctx, meter.ID, 2); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-circle get: %v", err)
	}
	if _, err := repo.AddReading(ctx, meter.ID, 2, 20, 2, false, nil); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-circle update: %v", err)
	}
	if _, err := repo.History(ctx, meter.ID, 2, 0); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-circle history: %v", err)
	}
	if err := repo.UpdateMetadata(ctx, meter.ID, 2, "stolen", mModel.UnitMiles); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-circle metadata: %v", err)
	}
	chore := &chModel.Chore{Name: "Other circle task", CircleID: 2, IsActive: true, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(chore).Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.UpsertSchedule(ctx, chore.ID, 2, mModel.ChoreScheduleRequest{MeterID: meter.ID, Interval: 10}); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("cross-circle attachment: %v", err)
	}
	current, _ := repo.Get(ctx, meter.ID, 1)
	if current.CurrentReading != 10 || current.Name != "Private Vehicle" {
		t.Fatalf("cross-circle attempts mutated meter: %#v", current)
	}
}

func TestMeterPersistenceAcrossDatabaseReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "meter-reload.db")
	open := func() (*Repository, *gorm.DB) {
		db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.AutoMigrate(&mModel.Meter{}, &mModel.Reading{}, &mModel.ChoreSchedule{}, &chModel.Chore{}, &syncModel.SyncCursor{}); err != nil {
			t.Fatal(err)
		}
		return NewRepository(db), db
	}
	ctx := context.Background()
	now := time.Date(2026, time.May, 1, 0, 0, 0, 0, time.UTC)
	repo, db := open()
	meter := &mModel.Meter{CircleID: 1, CreatedBy: 1, Name: "Persistent Odometer", Unit: mModel.UnitKilometers, CurrentReading: 80000, CreatedAt: now, UpdatedAt: now}
	if err := repo.Create(ctx, meter, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.AddReading(ctx, meter.ID, 1, 85000, 1, false, nil); err != nil {
		t.Fatal(err)
	}
	chore := &chModel.Chore{Name: "Persistent Oil", CircleID: 1, IsActive: true, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(chore).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&mModel.ChoreSchedule{ChoreID: chore.ID, MeterID: meter.ID, Interval: 10000, Baseline: 80000}).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	reloadedRepo, reloadedDB := open()
	reloaded, err := reloadedRepo.Get(ctx, meter.ID, 1)
	if err != nil {
		t.Fatal(err)
	}
	history, err := reloadedRepo.History(ctx, meter.ID, 1, 0)
	if err != nil {
		t.Fatal(err)
	}
	var schedule mModel.ChoreSchedule
	if err := reloadedDB.First(&schedule, "chore_id = ?", chore.ID).Error; err != nil {
		t.Fatal(err)
	}
	PopulateScheduleAt(&schedule, *reloaded, now)
	if reloaded.CurrentReading != 85000 || len(history) != 2 || schedule.MeterID != meter.ID || schedule.Baseline != 80000 || schedule.Target != 90000 {
		t.Fatalf("state not preserved: meter=%#v history=%d schedule=%#v", reloaded, len(history), schedule)
	}
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
