package chore

import (
	"context"
	"testing"
	"time"

	"donetick.com/core/config"
	chModel "donetick.com/core/internal/chore/model"
	cModel "donetick.com/core/internal/circle/model"
	mModel "donetick.com/core/internal/meter/model"
	syncModel "donetick.com/core/internal/sync/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestCompleteChoreCapturesMeterBaselineAndKeepsMeterOnlyActive(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&chModel.Chore{}, &chModel.ChoreHistory{}, &chModel.TimeSession{}, &cModel.UserCircle{}, &mModel.Meter{}, &mModel.ChoreSchedule{}, &syncModel.SyncCursor{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	meter := mModel.Meter{CircleID: 1, CreatedBy: 1, Name: "Vehicle", Unit: mModel.UnitKilometers, CurrentReading: 82000, CreatedAt: now, UpdatedAt: now}
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
	if err := repo.CompleteChore(context.Background(), &chore, nil, 1, nil, &now, nil, false); err != nil {
		t.Fatal(err)
	}
	var savedSchedule mModel.ChoreSchedule
	db.First(&savedSchedule, "chore_id = ?", chore.ID)
	if savedSchedule.Baseline != 82000 || savedSchedule.Baseline+savedSchedule.Interval != 92000 {
		t.Fatalf("next threshold not recalculated from completion: %#v", savedSchedule)
	}
	var history chModel.ChoreHistory
	db.Where("chore_id = ?", chore.ID).First(&history)
	if history.MeterID == nil || history.MeterReading == nil || *history.MeterReading != 82000 {
		t.Fatalf("completion meter snapshot missing: %#v", history)
	}
	var savedChore chModel.Chore
	db.First(&savedChore, chore.ID)
	if !savedChore.IsActive {
		t.Fatal("meter-only chore was archived after completion")
	}
}
