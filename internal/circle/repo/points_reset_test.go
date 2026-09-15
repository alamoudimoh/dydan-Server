package repo

import (
	"context"
	"testing"

	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	pModel "github.com/alamoudimoh/dydan-server/internal/points"
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
	if first.CurrentBalance() != 0 {
		t.Fatal("target balance is not zero")
	}
	if second.CurrentBalance() != -20 {
		t.Fatal("single reset affected another member")
	}
	count, err = repo.ResetPoints(context.Background(), 10, nil, 9)
	if err != nil || count != 1 {
		t.Fatalf("reset all should reset remaining non-zero member: count=%d err=%v", count, err)
	}
	for _, id := range []int{1, 2} {
		var membership cModel.UserCircle
		db.Where("circle_id = 10 AND user_id = ?", id).First(&membership)
		if membership.CurrentBalance() != 0 {
			t.Fatalf("user %d balance is not zero", id)
		}
	}
	var outside cModel.UserCircle
	db.Where("circle_id = 20 AND user_id = 3").First(&outside)
	if outside.Points != 99 || outside.PointsRedeemed != 0 || outside.PointsAdjusted != 0 {
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

func TestPointsAccountingKeepsResetSeparateFromRedemption(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&cModel.UserCircle{}, &pModel.PointsHistory{}); err != nil {
		t.Fatal(err)
	}

	members := []cModel.UserCircle{
		{UserID: 1, CircleID: 10, Points: 17},
		{UserID: 2, CircleID: 10, Points: 8},
		{UserID: 1, CircleID: 20, Points: 50},
	}
	if err := db.Create(&members).Error; err != nil {
		t.Fatal(err)
	}
	repo := &CircleRepository{db: db}
	ctx := context.Background()

	if err := repo.RedeemPoints(ctx, 10, 1, 5, 9); err != nil {
		t.Fatal(err)
	}
	assertPointsAccounting(t, db, 10, 1, 12, 5, 17, 0)

	target := 1
	count, err := repo.ResetPoints(ctx, 10, &target, 9)
	if err != nil || count != 1 {
		t.Fatalf("first reset: count=%d err=%v", count, err)
	}
	assertPointsAccounting(t, db, 10, 1, 0, 5, 17, 12)
	assertResetHistory(t, repo, 10, 1, []int{12})

	if err := db.Model(&cModel.UserCircle{}).
		Where("circle_id = ? AND user_id = ?", 10, 1).
		Update("points", gorm.Expr("points + ?", 10)).Error; err != nil {
		t.Fatal(err)
	}
	assertPointsAccounting(t, db, 10, 1, 10, 5, 27, 12)

	count, err = repo.ResetPoints(ctx, 10, &target, 9)
	if err != nil || count != 1 {
		t.Fatalf("second reset: count=%d err=%v", count, err)
	}
	assertPointsAccounting(t, db, 10, 1, 0, 5, 27, 22)
	assertResetHistory(t, repo, 10, 1, []int{10, 12})

	count, err = repo.ResetPoints(ctx, 10, &target, 9)
	if err != nil || count != 0 {
		t.Fatalf("zero-balance reset must be a no-op: count=%d err=%v", count, err)
	}
	assertPointsAccounting(t, db, 10, 1, 0, 5, 27, 22)

	allCircleHistory, err := repo.GetPointsHistory(ctx, 10, nil)
	if err != nil || len(allCircleHistory) != 3 {
		t.Fatalf("circle history filter returned %d entries: %v", len(allCircleHistory), err)
	}
	otherUser := 2
	otherHistory, err := repo.GetPointsHistory(ctx, 10, &otherUser)
	if err != nil || len(otherHistory) != 0 {
		t.Fatalf("user history filter leaked entries: %#v err=%v", otherHistory, err)
	}
	outsideCircleHistory, err := repo.GetPointsHistory(ctx, 20, nil)
	if err != nil || len(outsideCircleHistory) != 0 {
		t.Fatalf("circle history filter leaked entries: %#v err=%v", outsideCircleHistory, err)
	}
}

func assertPointsAccounting(t *testing.T, db *gorm.DB, circleID, userID, balance, redeemed, earned, adjusted int) {
	t.Helper()
	var membership cModel.UserCircle
	if err := db.Where("circle_id = ? AND user_id = ?", circleID, userID).First(&membership).Error; err != nil {
		t.Fatal(err)
	}
	if membership.CurrentBalance() != balance || membership.PointsRedeemed != redeemed || membership.Points != earned || membership.PointsAdjusted != adjusted {
		t.Fatalf("unexpected accounting: balance=%d redeemed=%d earned=%d adjusted=%d membership=%#v", balance, redeemed, earned, adjusted, membership)
	}
}

func assertResetHistory(t *testing.T, repo *CircleRepository, circleID, userID int, expectedBalances []int) {
	t.Helper()
	history, err := repo.GetPointsHistory(context.Background(), circleID, &userID)
	if err != nil {
		t.Fatal(err)
	}
	var resets []pModel.PointsHistory
	for _, entry := range history {
		if entry.Action == pModel.PointsHistoryActionReset {
			resets = append(resets, entry)
		}
	}
	if len(resets) != len(expectedBalances) {
		t.Fatalf("expected %d resets, got %#v", len(expectedBalances), resets)
	}
	for i, balance := range expectedBalances {
		if resets[i].PreviousBalance == nil || *resets[i].PreviousBalance != balance || resets[i].ResultingBalance == nil || *resets[i].ResultingBalance != 0 || resets[i].Points != -balance {
			t.Fatalf("reset %d is not auditable or has wrong category: %#v", i, resets[i])
		}
	}
}
