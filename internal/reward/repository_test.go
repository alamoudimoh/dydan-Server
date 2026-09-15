package reward

import (
	"context"
	"errors"
	"sync"
	"testing"

	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/dydan-server/internal/circle/repo"
	pModel "github.com/alamoudimoh/dydan-server/internal/points"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func testRepo(t *testing.T) (*Repository, *gorm.DB) {
	t.Helper()
	db, e := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_pragma=busy_timeout(5000)"), &gorm.Config{})
	if e != nil {
		t.Fatal(e)
	}
	if e = db.AutoMigrate(&uModel.User{}, &cModel.UserCircle{}, &Reward{}, &RewardRequest{}, &pModel.PointsHistory{}); e != nil {
		t.Fatal(e)
	}
	return NewRepository(db), db
}
func seed(t *testing.T, db *gorm.DB) {
	t.Helper()
	users := []uModel.User{{ID: 1, Username: "admin"}, {ID: 2, Username: "member"}, {ID: 3, Username: "other"}}
	if e := db.Create(&users).Error; e != nil {
		t.Fatal(e)
	}
	members := []cModel.UserCircle{{UserID: 1, CircleID: 10, Role: cModel.UserRoleAdmin, IsActive: true}, {UserID: 2, CircleID: 10, Role: cModel.UserRoleMember, IsActive: true, Points: 150}, {UserID: 3, CircleID: 10, Role: cModel.UserRoleMember, IsActive: true, Points: 150}, {UserID: 1, CircleID: 20, Role: cModel.UserRoleAdmin, IsActive: true}}
	if e := db.Create(&members).Error; e != nil {
		t.Fatal(e)
	}
}
func TestRewardLifecycleAccountingSnapshotsAndReservations(t *testing.T) {
	r, db := testRepo(t)
	seed(t, db)
	ctx := context.Background()
	reward := Reward{CircleID: 10, Name: "Dinner", CostPoints: 100, IsActive: true, CreatedBy: 1}
	if e := r.Create(ctx, &reward); e != nil {
		t.Fatal(e)
	}
	listed, e := r.ListRewards(ctx, 10, false)
	if e != nil || len(listed) != 1 || listed[0].ID != reward.ID {
		t.Fatalf("catalog list: %+v %v", listed, e)
	}
	req, e := r.Request(ctx, 10, 2, reward.ID)
	if e != nil {
		t.Fatal(e)
	}
	if req.Status != RequestStatusPending || req.RewardNameSnapshot != "Dinner" || req.CostPointsSnapshot != 100 {
		t.Fatalf("bad snapshot: %+v", req)
	}
	b, e := r.Balance(ctx, 10, 2)
	if e != nil || b.Current != 150 || b.Reserved != 100 || b.Spendable != 50 {
		t.Fatalf("bad balance: %+v %v", b, e)
	}
	var count int64
	db.Model(&pModel.PointsHistory{}).Count(&count)
	if count != 0 {
		t.Fatal("pending changed ledger")
	}
	if _, e = r.Request(ctx, 10, 2, reward.ID); !errors.Is(e, ErrInsufficient) {
		t.Fatalf("overspend = %v", e)
	}
	cheap := Reward{CircleID: 10, Name: "Treat", CostPoints: 50, IsActive: true, CreatedBy: 1}
	r.Create(ctx, &cheap)
	if _, e = r.Request(ctx, 10, 2, cheap.ID); e != nil {
		t.Fatalf("exact boundary: %v", e)
	}
	other, e := r.Balance(ctx, 10, 3)
	if e != nil || other.Spendable != 150 {
		t.Fatalf("other reservation leaked: %+v", other)
	}
	active := true
	updated, e := r.Update(ctx, 10, reward.ID, "New dinner", "", 150, &active)
	if e != nil || updated.CostPoints != 150 {
		t.Fatal(e)
	}
	approved, e := r.Resolve(ctx, 10, req.ID, 1, RequestStatusApproved)
	if e != nil || approved.CostPointsSnapshot != 100 {
		t.Fatalf("approve: %+v %v", approved, e)
	}
	var member cModel.UserCircle
	db.Where("circle_id=10 AND user_id=2").First(&member)
	if member.CurrentBalance() != 50 || member.PointsRedeemed != 100 || member.Points != 150 {
		t.Fatalf("accounting: %+v", member)
	}
	db.Model(&pModel.PointsHistory{}).Where("action=?", pModel.PointsHistoryActionRedeem).Count(&count)
	if count != 1 {
		t.Fatalf("redeem entries=%d", count)
	}
	if _, e = r.Resolve(ctx, 10, req.ID, 1, RequestStatusApproved); !errors.Is(e, ErrResolved) {
		t.Fatalf("double approval=%v", e)
	}
}
func TestRejectArchiveResetAndStateTransitions(t *testing.T) {
	r, db := testRepo(t)
	seed(t, db)
	ctx := context.Background()
	rw := Reward{CircleID: 10, Name: "Reward", CostPoints: 80, IsActive: true, CreatedBy: 1}
	r.Create(ctx, &rw)
	req, _ := r.Request(ctx, 10, 2, rw.ID)
	inactive := false
	r.Update(ctx, 10, rw.ID, rw.Name, "", rw.CostPoints, &inactive)
	if _, e := r.Request(ctx, 10, 3, rw.ID); !errors.Is(e, ErrInactive) {
		t.Fatalf("archive=%v", e)
	}
	if _, e := r.Resolve(ctx, 10, req.ID, 1, RequestStatusRejected); e != nil {
		t.Fatal(e)
	}
	var m cModel.UserCircle
	db.Where("circle_id=10 AND user_id=2").First(&m)
	if m.CurrentBalance() != 150 || m.PointsRedeemed != 0 {
		t.Fatal("rejection changed points")
	}
	b, _ := r.Balance(ctx, 10, 2)
	if b.Reserved != 0 {
		t.Fatal("rejection did not release")
	}
	if _, e := r.Resolve(ctx, 10, req.ID, 1, RequestStatusApproved); !errors.Is(e, ErrResolved) {
		t.Fatal("approved rejected request")
	}
	inactive = true
	r.Update(ctx, 10, rw.ID, rw.Name, "", rw.CostPoints, &inactive)
	req, _ = r.Request(ctx, 10, 2, rw.ID)
	db.Model(&cModel.UserCircle{}).Where("circle_id=10 AND user_id=2").Update("points_adjusted", 150)
	if _, e := r.Resolve(ctx, 10, req.ID, 1, RequestStatusApproved); !errors.Is(e, ErrInsufficient) {
		t.Fatalf("approval after reset=%v", e)
	}
	var got RewardRequest
	db.First(&got, req.ID)
	if got.Status != RequestStatusPending {
		t.Fatalf("failed approval resolved request: %s", got.Status)
	}
}
func TestConcurrentResolutionRedeemsOnce(t *testing.T) {
	r, db := testRepo(t)
	seed(t, db)
	rw := Reward{CircleID: 10, Name: "Reward", CostPoints: 100, IsActive: true, CreatedBy: 1}
	r.Create(context.Background(), &rw)
	req, _ := r.Request(context.Background(), 10, 2, rw.ID)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, s := range []RequestStatus{RequestStatusApproved, RequestStatusRejected} {
		wg.Add(1)
		go func(status RequestStatus) {
			defer wg.Done()
			_, e := r.Resolve(context.Background(), 10, req.ID, 1, status)
			errs <- e
		}(s)
	}
	wg.Wait()
	close(errs)
	success := 0
	for e := range errs {
		if e == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("successful resolutions=%d", success)
	}
	var count int64
	db.Model(&pModel.PointsHistory{}).Where("reward_request_id=?", req.ID).Count(&count)
	if count > 1 {
		t.Fatalf("redeemed %d times", count)
	}
}

func TestRewardApprovalPreservesResetAccountingSemantics(t *testing.T) {
	r, db := testRepo(t)
	seed(t, db)
	ctx := context.Background()
	if err := db.Model(&cModel.UserCircle{}).Where("circle_id = ? AND user_id = ?", 10, 2).Updates(map[string]any{"points": 17, "points_redeemed": 0, "points_adjusted": 0}).Error; err != nil {
		t.Fatal(err)
	}
	circles := cRepo.NewCircleRepository(db)
	if err := circles.RedeemPoints(ctx, 10, 2, 5, 1); err != nil {
		t.Fatal(err)
	}
	target := 2
	if _, err := circles.ResetPoints(ctx, 10, &target, 1); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&cModel.UserCircle{}).Where("circle_id = ? AND user_id = ?", 10, 2).Update("points", gorm.Expr("points + ?", 100)).Error; err != nil {
		t.Fatal(err)
	}
	reward := Reward{CircleID: 10, Name: "Reward", CostPoints: 40, IsActive: true, CreatedBy: 1}
	if err := r.Create(ctx, &reward); err != nil {
		t.Fatal(err)
	}
	request, err := r.Request(ctx, 10, 2, reward.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = r.Resolve(ctx, 10, request.ID, 1, RequestStatusApproved); err != nil {
		t.Fatal(err)
	}
	var membership cModel.UserCircle
	if err := db.Where("circle_id = ? AND user_id = ?", 10, 2).First(&membership).Error; err != nil {
		t.Fatal(err)
	}
	if membership.CurrentBalance() != 60 || membership.PointsRedeemed != 45 || membership.Points != 117 || membership.PointsAdjusted != 12 {
		t.Fatalf("incorrect final accounting: %+v", membership)
	}
	var resetCount, redeemCount int64
	db.Model(&pModel.PointsHistory{}).Where("action = ?", pModel.PointsHistoryActionReset).Count(&resetCount)
	db.Model(&pModel.PointsHistory{}).Where("action = ?", pModel.PointsHistoryActionRedeem).Count(&redeemCount)
	if resetCount != 1 || redeemCount != 2 {
		t.Fatalf("reset=%d redeem=%d", resetCount, redeemCount)
	}
}
