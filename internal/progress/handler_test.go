package progress

import (
	"fmt"
	"testing"
	"time"

	chModel "donetick.com/core/internal/chore/model"
	pModel "donetick.com/core/internal/points"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func testHandler(t *testing.T) (*Handler, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err = db.AutoMigrate(&chModel.Chore{}, &chModel.ChoreHistory{}, &pModel.PointsHistory{}, &Goal{}); err != nil {
		t.Fatal(err)
	}
	return &Handler{db: db}, db
}

func TestGoalProgressUsesAuthoritativeHistoryAndCircleRange(t *testing.T) {
	h, db := testHandler(t)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	cat, other := 7, 8
	chores := []chModel.Chore{{ID: 1, CircleID: 10, CategoryID: &cat}, {ID: 2, CircleID: 10, CategoryID: &other}, {ID: 3, CircleID: 20, CategoryID: &cat}}
	if err := db.Create(&chores).Error; err != nil {
		t.Fatal(err)
	}
	in, outside := start.Add(time.Hour), end.Add(time.Hour)
	history := []chModel.ChoreHistory{
		{ChoreID: 1, Status: chModel.ChoreHistoryStatusCompleted, PerformedAt: &in},
		{ChoreID: 1, Status: chModel.ChoreHistoryStatusCompleted, PerformedAt: &in},
		{ChoreID: 2, Status: chModel.ChoreHistoryStatusCompleted, PerformedAt: &in},
		{ChoreID: 3, Status: chModel.ChoreHistoryStatusCompleted, PerformedAt: &in},
		{ChoreID: 1, Status: chModel.ChoreHistoryStatusCompleted, PerformedAt: &outside},
		{ChoreID: 1, Status: chModel.ChoreHistoryStatusMissed, PerformedAt: &in},
	}
	if err := db.Create(&history).Error; err != nil {
		t.Fatal(err)
	}
	if got := h.progress(Goal{CircleID: 10, MetricType: MetricTaskCompletions, StartDate: start, EndDate: end}); got != 3 {
		t.Fatalf("task progress=%d, want 3", got)
	}
	if got := h.progress(Goal{CircleID: 10, MetricType: MetricCategoryCompletions, CategoryID: &cat, StartDate: start, EndDate: end}); got != 2 {
		t.Fatalf("category progress=%d, want 2", got)
	}
}

func TestPointsGoalCountsAddOnly(t *testing.T) {
	h, db := testHandler(t)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	entries := []pModel.PointsHistory{
		{CircleID: 10, UserID: 1, Action: pModel.PointsHistoryActionAdd, Points: 50, CreatedAt: start.Add(time.Hour)},
		{CircleID: 10, UserID: 1, Action: pModel.PointsHistoryActionRedeem, Points: -20, CreatedAt: start.Add(time.Hour)},
		{CircleID: 10, UserID: 1, Action: pModel.PointsHistoryActionReset, Points: -30, CreatedAt: start.Add(time.Hour)},
		{CircleID: 20, UserID: 1, Action: pModel.PointsHistoryActionAdd, Points: 100, CreatedAt: start.Add(time.Hour)},
	}
	if err := db.Create(&entries).Error; err != nil {
		t.Fatal(err)
	}
	if got := h.progress(Goal{CircleID: 10, MetricType: MetricPointsEarned, StartDate: start, EndDate: end}); got != 50 {
		t.Fatalf("points progress=%d, want 50", got)
	}
}

func TestGoalCompletionTransitionIsIdempotent(t *testing.T) {
	h, db := testHandler(t)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	performed := start.Add(time.Hour)
	db.Create(&chModel.Chore{ID: 1, CircleID: 10})
	db.Create(&chModel.ChoreHistory{ChoreID: 1, Status: chModel.ChoreHistoryStatusCompleted, PerformedAt: &performed})
	g := Goal{CircleID: 10, Name: "Reset week", MetricType: MetricTaskCompletions, Target: 1, StartDate: start, EndDate: end, Status: GoalActive, CreatedBy: 1}
	if err := db.Create(&g).Error; err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if h.progress(g) >= g.Target {
			db.Model(&Goal{}).Where("id=? AND status=?", g.ID, GoalActive).Update("status", GoalCompleted)
		}
	}
	var stored Goal
	db.First(&stored, g.ID)
	if stored.Status != GoalCompleted {
		t.Fatalf("status=%s", stored.Status)
	}
	var count int64
	db.Model(&Goal{}).Where("id=?", g.ID).Count(&count)
	if count != 1 {
		t.Fatalf("goal duplicated: %d", count)
	}
}
