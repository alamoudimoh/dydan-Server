package migrations

import (
	"context"
	"testing"

	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	pModel "github.com/alamoudimoh/dydan-server/internal/points"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSeparateResetAdjustmentsBackfillsExistingData(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&cModel.UserCircle{}, &pModel.PointsHistory{}); err != nil {
		t.Fatal(err)
	}
	membership := cModel.UserCircle{UserID: 1, CircleID: 10, Points: 27, PointsRedeemed: 27}
	if err := db.Create(&membership).Error; err != nil {
		t.Fatal(err)
	}
	first, zero := 12, 0
	second := 10
	history := []pModel.PointsHistory{
		{Action: pModel.PointsHistoryActionRedeem, CircleID: 10, UserID: 1, Points: 5},
		{Action: pModel.PointsHistoryActionReset, CircleID: 10, UserID: 1, Points: -12, PreviousBalance: &first, ResultingBalance: &zero},
		{Action: pModel.PointsHistoryActionReset, CircleID: 10, UserID: 1, Points: -10, PreviousBalance: &second, ResultingBalance: &zero},
	}
	if err := db.Create(&history).Error; err != nil {
		t.Fatal(err)
	}
	if err := (SeparateResetAdjustments20260810{}).Up(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	var got cModel.UserCircle
	if err := db.First(&got, membership.ID).Error; err != nil {
		t.Fatal(err)
	}
	if got.Points != 27 || got.PointsRedeemed != 5 || got.PointsAdjusted != 22 || got.CurrentBalance() != 0 {
		t.Fatalf("historical reset data was not repaired: %#v", got)
	}
	var count int64
	db.Model(&pModel.PointsHistory{}).Count(&count)
	if count != 3 {
		t.Fatalf("migration changed audit history: count=%d", count)
	}
}
