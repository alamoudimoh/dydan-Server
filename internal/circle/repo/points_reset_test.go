package repo

import (
	"context"
	"testing"

	cModel "donetick.com/core/internal/circle/model"
	pModel "donetick.com/core/internal/points"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestResetPointsPreservesLedgerAndCircleBoundary(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&cModel.UserCircle{}, &pModel.PointsHistory{}); err != nil {
		t.Fatal(err)
	}
	members := []cModel.UserCircle{{UserID: 1, CircleID: 10, Points: 350}, {UserID: 2, CircleID: 10, Points: 20, PointsRedeemed: 40}, {UserID: 3, CircleID: 20, Points: 99}}
	if err := db.Create(&members).Error; err != nil {
		t.Fatal(err)
	}
	old := pModel.PointsHistory{Action: pModel.PointsHistoryActionAdd, Points: 350, UserID: 1, CircleID: 10, CreatedBy: 1}
	db.Create(&old)
	repo := &CircleRepository{db: db}
	target := 1
	count, err := repo.ResetPoints(context.Background(), 10, &target, 9)
	if err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("expected one reset, got %d", count)
	}
	var first, second cModel.UserCircle
	db.Where("circle_id = 10 AND user_id = 1").First(&first)
	db.Where("circle_id = 10 AND user_id = 2").First(&second)
	if first.Points-first.PointsRedeemed != 0 {
		t.Fatal("target balance is not zero")
	}
	if second.Points-second.PointsRedeemed != -20 {
		t.Fatal("single reset affected another member")
	}
	count, err = repo.ResetPoints(context.Background(), 10, nil, 9)
	if err != nil || count != 1 {
		t.Fatalf("reset all should reset remaining non-zero member: count=%d err=%v", count, err)
	}
	for _, id := range []int{1, 2} {
		var membership cModel.UserCircle
		db.Where("circle_id = 10 AND user_id = ?", id).First(&membership)
		if membership.Points-membership.PointsRedeemed != 0 {
			t.Fatalf("user %d balance is not zero", id)
		}
	}
	var outside cModel.UserCircle
	db.Where("circle_id = 20 AND user_id = 3").First(&outside)
	if outside.Points != 99 || outside.PointsRedeemed != 0 {
		t.Fatal("outside circle changed")
	}
	var histories []pModel.PointsHistory
	db.Where("circle_id = 10").Order("id").Find(&histories)
	if len(histories) != 3 {
		t.Fatalf("old ledger entry was not preserved or resets missing: %d", len(histories))
	}
	if histories[1].Action != pModel.PointsHistoryActionReset || histories[1].CreatedBy != 9 || histories[1].PreviousBalance == nil || *histories[1].ResultingBalance != 0 {
		t.Fatalf("reset audit incomplete: %#v", histories[1])
	}
	if histories[2].PreviousBalance == nil || *histories[2].PreviousBalance != -20 || histories[2].Points != 20 || *histories[2].ResultingBalance != 0 {
		t.Fatalf("negative balance reset is incorrect: %#v", histories[2])
	}
	zeroTarget := 1
	count, err = repo.ResetPoints(context.Background(), 10, &zeroTarget, 9)
	if err != nil || count != 0 {
		t.Fatalf("zero reset should be no-op: count=%d err=%v", count, err)
	}
}
