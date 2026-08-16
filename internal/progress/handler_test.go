package progress

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	chModel "github.com/alamoudimoh/daydan-server/internal/chore/model"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/daydan-server/internal/circle/repo"
	lModel "github.com/alamoudimoh/daydan-server/internal/label/model"
	pModel "github.com/alamoudimoh/daydan-server/internal/points"
	tModel "github.com/alamoudimoh/daydan-server/internal/thing/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
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

func TestWorkloadContractReturnsArraysAndPreservesPrivateTaskVisibility(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&chModel.Chore{}, &chModel.ChoreAssignees{}, &cModel.UserCircle{}, &lModel.Label{}, &tModel.ThingChore{}, &uModel.User{}); err != nil {
		t.Fatal(err)
	}
	clock := time.Date(2026, 8, 13, 12, 0, 0, 0, time.UTC)
	h := &Handler{db: db, circles: cRepo.NewCircleRepository(db), now: func() time.Time { return clock }}
	users := []uModel.User{
		{ID: 1, Username: "manager", CircleID: 10, Timezone: "UTC"},
		{ID: 2, Username: "member", CircleID: 10, Timezone: "UTC"},
	}
	memberships := []cModel.UserCircle{
		{UserID: 1, CircleID: 10, Role: cModel.UserRoleAdmin, IsActive: true},
		{UserID: 2, CircleID: 10, Role: cModel.UserRoleMember, IsActive: true},
	}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&memberships).Error; err != nil {
		t.Fatal(err)
	}
	due := clock.Add(24 * time.Hour)
	memberID := 2
	chores := []chModel.Chore{
		{ID: 1, Name: "Shared unassigned", CircleID: 10, CreatedBy: 1, IsActive: true, NextDueDate: &due},
		{ID: 2, Name: "Private detail", CircleID: 10, CreatedBy: 2, AssignedTo: &memberID, IsPrivate: true, IsActive: true, NextDueDate: &due},
	}
	if err := db.Create(&chores).Error; err != nil {
		t.Fatal(err)
	}

	request := func(user uModel.User) (int, map[string]json.RawMessage, string) {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		c.Request = httptest.NewRequest(http.MethodGet, "/api/v1/progress/workload?range=week", nil)
		c.Set("id", &uModel.UserDetails{User: user})
		h.workload(c)
		var payload map[string]json.RawMessage
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return w.Code, payload, w.Body.String()
	}

	code, managerPayload, managerBody := request(users[0])
	if code != http.StatusOK || string(managerPayload["tasks"])[0] != '[' || string(managerPayload["members"])[0] != '[' || string(managerPayload["categories"])[0] != '[' {
		t.Fatalf("workload collections must always be arrays: %s", managerBody)
	}
	if !strings.Contains(managerBody, "Shared unassigned") || strings.Contains(managerBody, "Private detail") || !strings.Contains(managerBody, `"unassigned":1`) {
		t.Fatalf("manager workload violated privacy or unassigned contract: %s", managerBody)
	}

	code, memberPayload, memberBody := request(users[1])
	if code != http.StatusOK || string(memberPayload["tasks"])[0] != '[' || !strings.Contains(memberBody, "Private detail") || strings.Contains(memberBody, "Shared unassigned") {
		t.Fatalf("member workload visibility mismatch: %s", memberBody)
	}
}

func streakRow(due, performed *time.Time, status chModel.ChoreHistoryStatus) struct {
	DueDate     *time.Time
	PerformedAt *time.Time
	Status      int
} {
	return struct {
		DueDate     *time.Time
		PerformedAt *time.Time
		Status      int
	}{due, performed, int(status)}
}

func TestStreakExpectedDaysAndFreeDays(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	day1 := time.Date(2026, 8, 10, 9, 0, 0, 0, loc)
	day3 := day1.AddDate(0, 0, 2)
	rows := []struct {
		DueDate     *time.Time
		PerformedAt *time.Time
		Status      int
	}{
		streakRow(&day1, &day1, chModel.ChoreHistoryStatusCompleted),
		streakRow(&day1, &day1, chModel.ChoreHistoryStatusCompleted),
		streakRow(&day3, &day3, chModel.ChoreHistoryStatusCompleted),
	}
	current, best, last := calculateStreak(rows, loc, day3.Add(time.Hour))
	if current != 2 || best != 2 || last != "2026-08-12" {
		t.Fatalf("current=%d best=%d last=%q, want 2,2,2026-08-12", current, best, last)
	}
}

func TestStreakMissedWorkBreaksButCurrentDayDoesNotPrematurelyBreak(t *testing.T) {
	loc, _ := time.LoadLocation("America/New_York")
	success := time.Date(2026, 3, 7, 23, 30, 0, 0, loc)
	missed := time.Date(2026, 3, 8, 9, 0, 0, 0, loc) // DST transition day.
	today := time.Date(2026, 3, 9, 8, 0, 0, 0, loc)
	rows := []struct {
		DueDate     *time.Time
		PerformedAt *time.Time
		Status      int
	}{
		streakRow(&success, &success, chModel.ChoreHistoryStatusCompleted),
		streakRow(&missed, nil, chModel.ChoreHistoryStatusMissed),
		streakRow(&today, nil, chModel.ChoreHistoryStatusMissed),
	}
	current, best, _ := calculateStreak(rows, loc, today)
	if current != 0 || best != 1 {
		t.Fatalf("current=%d best=%d, want 0,1", current, best)
	}

	rows = rows[:1]
	rows = append(rows, streakRow(&today, nil, chModel.ChoreHistoryStatusMissed))
	current, best, _ = calculateStreak(rows, loc, today)
	if current != 1 || best != 1 {
		t.Fatalf("unfinished current day erased streak: current=%d best=%d", current, best)
	}
}

func TestStreakRiyadhMidnightUsesLocalCalendarDay(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	due := time.Date(2026, 8, 13, 0, 15, 0, 0, loc)
	performed := due.Add(15 * time.Minute)
	current, best, last := calculateStreak([]struct {
		DueDate     *time.Time
		PerformedAt *time.Time
		Status      int
	}{streakRow(&due, &performed, chModel.ChoreHistoryStatusCompleted)}, loc, performed)
	if current != 1 || best != 1 || last != "2026-08-13" {
		t.Fatalf("Riyadh boundary current=%d best=%d last=%q", current, best, last)
	}
}

func TestParseBoundsUsesInjectedTimezoneClock(t *testing.T) {
	loc, _ := time.LoadLocation("Asia/Riyadh")
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest("GET", "/?range=today", nil)
	clock := time.Date(2026, 8, 12, 21, 15, 0, 0, time.UTC) // Aug 13 00:15 Riyadh.
	b, ok := parseBounds(c, loc, false, clock)
	if !ok || b.Start.In(loc).Format("2006-01-02 15:04") != "2026-08-13 00:00" || b.End.Sub(b.Start) != 24*time.Hour {
		t.Fatalf("unexpected Riyadh bounds: %#v", b)
	}
}

func TestWorkloadAndReportRangesHaveTruthfulDirection(t *testing.T) {
	loc := time.UTC
	clock := time.Date(2026, 8, 13, 12, 0, 0, 0, loc)
	workloadContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	workloadContext.Request = httptest.NewRequest("GET", "/?range=30d", nil)
	workload, ok := parseBounds(workloadContext, loc, true, clock)
	if !ok || workload.Start != time.Date(2026, 8, 13, 0, 0, 0, 0, loc) || workload.End != time.Date(2026, 9, 12, 0, 0, 0, 0, loc) {
		t.Fatalf("next-30-day workload range is not forward-looking: %#v", workload)
	}
	reportContext, _ := gin.CreateTestContext(httptest.NewRecorder())
	reportContext.Request = httptest.NewRequest("GET", "/?range=7d", nil)
	report, ok := parseBounds(reportContext, loc, false, clock)
	if !ok || report.Start != time.Date(2026, 8, 7, 0, 0, 0, 0, loc) || report.End != time.Date(2026, 8, 14, 0, 0, 0, 0, loc) {
		t.Fatalf("seven-day report range is not trailing: %#v", report)
	}
}

func TestSummarizeWorkloadCountsActionableMembersUnassignedAndCategories(t *testing.T) {
	today := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	tomorrow := today.AddDate(0, 0, 1)
	end := today.AddDate(0, 0, 7)
	memberID, categoryID := 1, 7
	overdue := today.Add(-time.Hour)
	dueToday := today.Add(time.Hour)
	upcoming := tomorrow.Add(time.Hour)
	tasks := []taskRow{
		{ID: 1, AssignedTo: &memberID, CategoryID: &categoryID, DueDate: &overdue},
		{ID: 2, AssignedTo: &memberID, CategoryID: &categoryID, DueDate: &dueToday, Maintenance: true},
		{ID: 3, AssignedTo: &memberID, DueDate: &upcoming},
		{ID: 4, DueDate: &upcoming},
	}
	members := []workloadRow{{UserID: memberID, Name: "Member"}}
	unassigned, categories := summarizeWorkload(tasks, members, map[int]int{memberID: 0}, today, tomorrow, bounds{Start: today, End: end})
	if members[0].Overdue != 1 || members[0].Today != 1 || members[0].Upcoming != 1 || members[0].Active != 3 || members[0].Maintenance != 1 {
		t.Fatalf("unexpected member workload: %#v", members[0])
	}
	if unassigned != 1 || categories["7"].Count != 2 || categories["uncategorized"].Count != 2 {
		t.Fatalf("unexpected unassigned/categories: %d %#v", unassigned, categories)
	}
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

func TestGoalProgressesAggregatesAllGoalsWithoutPerGoalQueries(t *testing.T) {
	h, db := testHandler(t)
	start := time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC)
	end := start.AddDate(0, 1, 0)
	cat := 7
	goals := []Goal{
		{CircleID: 10, Name: "Tasks", MetricType: MetricTaskCompletions, Target: 5, StartDate: start, EndDate: end, Status: GoalActive},
		{CircleID: 10, Name: "Home", MetricType: MetricCategoryCompletions, CategoryID: &cat, Target: 5, StartDate: start, EndDate: end, Status: GoalActive},
		{CircleID: 10, Name: "Points", MetricType: MetricPointsEarned, Target: 100, StartDate: start, EndDate: end, Status: GoalActive},
	}
	if err := db.Create(&goals).Error; err != nil {
		t.Fatal(err)
	}
	other := 8
	db.Create(&[]chModel.Chore{{ID: 1, CircleID: 10, CategoryID: &cat}, {ID: 2, CircleID: 10, CategoryID: &other}})
	performed := start.Add(time.Hour)
	db.Create(&[]chModel.ChoreHistory{
		{ChoreID: 1, Status: chModel.ChoreHistoryStatusCompleted, PerformedAt: &performed},
		{ChoreID: 2, Status: chModel.ChoreHistoryStatusCompleted, PerformedAt: &performed},
	})
	db.Create(&[]pModel.PointsHistory{
		{CircleID: 10, Action: pModel.PointsHistoryActionAdd, Points: 40, CreatedAt: performed},
		{CircleID: 10, Action: pModel.PointsHistoryActionReset, Points: -40, CreatedAt: performed},
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	got := h.goalProgresses(c, 10)
	if got[goals[0].ID] != 2 || got[goals[1].ID] != 1 || got[goals[2].ID] != 40 {
		t.Fatalf("unexpected aggregate progress: %#v", got)
	}
}
