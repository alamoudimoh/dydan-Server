package chore

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	chModel "github.com/alamoudimoh/daydan-server/internal/chore/model"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	pModel "github.com/alamoudimoh/daydan-server/internal/points"
	syncModel "github.com/alamoudimoh/daydan-server/internal/sync/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func hardeningRepository(t *testing.T) (*ChoreRepository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&chModel.Chore{}, &chModel.ChoreHistory{}, &chModel.TimeSession{}, &cModel.UserCircle{}, &pModel.PointsHistory{}, &syncModel.SyncCursor{}); err != nil {
		t.Fatal(err)
	}
	return NewChoreRepository(db, &config.Config{Database: config.DatabaseConfig{Type: "sqlite"}}), db
}

func TestCreateChoreIdempotentReusesDraftKey(t *testing.T) {
	repository, db := hardeningRepository(t)
	key := "7bb89375-221d-4df6-8fa7-928f82415ca7"
	first := &chModel.Chore{Name: "Laundry", CircleID: 2, CreatedBy: 3, UpdatedBy: 3, FrequencyType: chModel.FrequencyTypeOnce, Frequency: 1, CreateKey: &key}
	firstID, created, err := repository.CreateChoreIdempotent(context.Background(), first)
	if err != nil || !created {
		t.Fatalf("first create = id %d created %v err %v", firstID, created, err)
	}
	retry := &chModel.Chore{Name: "Laundry retry", CircleID: 2, CreatedBy: 3, UpdatedBy: 3, FrequencyType: chModel.FrequencyTypeOnce, Frequency: 1, CreateKey: &key}
	retryID, created, err := repository.CreateChoreIdempotent(context.Background(), retry)
	if err != nil || created || retryID != firstID {
		t.Fatalf("retry = id %d created %v err %v; want id %d replay", retryID, created, err, firstID)
	}
	var count int64
	db.Model(&chModel.Chore{}).Count(&count)
	if count != 1 {
		t.Fatalf("chore count = %d, want 1", count)
	}
}

func TestCompletionOperationAndVoidAreAuditable(t *testing.T) {
	repository, db := hardeningRepository(t)
	now := time.Now().UTC()
	points := 5
	chore := chModel.Chore{Name: "Dishes", CircleID: 4, CreatedBy: 1, UpdatedBy: 1, FrequencyType: chModel.FrequencyTypeOnce, Frequency: 1, IsActive: true, Points: &points}
	if err := db.Create(&chore).Error; err != nil {
		t.Fatal(err)
	}
	membership := cModel.UserCircle{UserID: 1, CircleID: 4, Role: cModel.UserRoleAdmin, IsActive: true}
	if err := db.Create(&membership).Error; err != nil {
		t.Fatal(err)
	}
	operationID := "fb47575e-f8e8-44c6-bf0e-0d8cd98f0df5"
	if err := repository.CompleteChore(context.Background(), &chore, nil, 1, nil, &now, nil, true, &operationID); err != nil {
		t.Fatal(err)
	}
	if err := repository.CompleteChore(context.Background(), &chore, nil, 1, nil, &now, nil, true, &operationID); !errors.Is(err, ErrCompletionAlreadyApplied) {
		t.Fatalf("duplicate completion error = %v, want %v", err, ErrCompletionAlreadyApplied)
	}
	var completionCount int64
	db.Model(&chModel.ChoreHistory{}).Where("operation_id = ?", operationID).Count(&completionCount)
	if completionCount != 1 {
		t.Fatalf("completion count = %d, want 1", completionCount)
	}
	if err := db.First(&membership, membership.ID).Error; err != nil {
		t.Fatal(err)
	}
	if membership.Points != points {
		t.Fatalf("points after duplicate completion = %d, want %d", membership.Points, points)
	}
	history, err := repository.GetCompletionByOperation(context.Background(), chore.ID, 4, operationID)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.VoidChoreHistory(context.Background(), history.ID, 4, 9, "Incorrect completion"); err != nil {
		t.Fatal(err)
	}
	var saved chModel.ChoreHistory
	if err := db.First(&saved, history.ID).Error; err != nil {
		t.Fatalf("history was deleted: %v", err)
	}
	if saved.Status != chModel.ChoreHistoryStatusVoided || saved.VoidedBy == nil || *saved.VoidedBy != 9 || saved.VoidReason == nil || *saved.VoidReason != "Incorrect completion" {
		t.Fatalf("void audit metadata missing: %#v", saved)
	}
	if err := db.First(&membership, membership.ID).Error; err != nil {
		t.Fatal(err)
	}
	if membership.Points != 0 {
		t.Fatalf("points after void = %d, want 0", membership.Points)
	}
	var removal pModel.PointsHistory
	if err := db.Where("action = ? AND user_id = ?", pModel.PointsHistoryActionRemove, 1).First(&removal).Error; err != nil {
		t.Fatalf("point reversal audit missing: %v", err)
	}
	if err := repository.VoidChoreHistory(context.Background(), history.ID, 4, 9, "retry"); err != nil {
		t.Fatal(err)
	}
	var removals int64
	db.Model(&pModel.PointsHistory{}).Where("action = ?", pModel.PointsHistoryActionRemove).Count(&removals)
	if removals != 1 {
		t.Fatalf("void retry created %d reversals, want 1", removals)
	}
}
