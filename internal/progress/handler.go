package progress

import (
	"strconv"
	"strings"
	"time"

	"github.com/alamoudimoh/daydan-server/internal/auth"
	chModel "github.com/alamoudimoh/daydan-server/internal/chore/model"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/daydan-server/internal/circle/repo"
	pModel "github.com/alamoudimoh/daydan-server/internal/points"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Handler struct {
	db      *gorm.DB
	circles *cRepo.CircleRepository
	now     func() time.Time
}

func NewHandler(db *gorm.DB, circles *cRepo.CircleRepository) *Handler {
	return &Handler{db: db, circles: circles, now: func() time.Time { return time.Now().UTC() }}
}

func (h *Handler) currentTime() time.Time {
	if h.now != nil {
		return h.now().UTC()
	}
	return time.Now().UTC()
}

type bounds struct {
	Start, End time.Time
	Location   *time.Location
}

func (h *Handler) context(c *gin.Context) (int, int, cModel.Role, *time.Location, bool) {
	u, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(401, gin.H{"error": "Authentication required"})
		return 0, 0, "", nil, false
	}
	role, err := h.circles.GetActiveUserCircleRole(c, u.CircleID, u.ID)
	if err != nil {
		c.JSON(403, gin.H{"error": "Active Circle membership required"})
		return 0, 0, "", nil, false
	}
	// Managed (child) accounts never exercise elevated capabilities even if a
	// stale or inconsistent membership row grants them an elevated role.
	role = auth.CapEffectiveCircleRole(&u.User, role)
	var tz string
	_ = h.db.WithContext(c).Table("users").Select("timezone").Where("id = ?", u.ID).Scan(&tz).Error
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return u.ID, u.CircleID, role, loc, true
}
func parseBounds(c *gin.Context, loc *time.Location, workload bool, clock time.Time) (bounds, bool) {
	now := clock.UTC().In(loc)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc)
	rangeName := c.DefaultQuery("range", map[bool]string{true: "week", false: "30d"}[workload])
	var start, end time.Time
	switch rangeName {
	case "today":
		start, end = day, day.AddDate(0, 0, 1)
	case "week":
		start = day.AddDate(0, 0, -int(day.Weekday()))
		end = start.AddDate(0, 0, 7)
	case "7d":
		start, end = day.AddDate(0, 0, -6), day.AddDate(0, 0, 1)
	case "month":
		start = time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, loc)
		end = start.AddDate(0, 1, 0)
	case "30d":
		if workload {
			start, end = day, day.AddDate(0, 0, 30)
		} else {
			start, end = day.AddDate(0, 0, -29), day.AddDate(0, 0, 1)
		}
	case "90d":
		start, end = day.AddDate(0, 0, -89), day.AddDate(0, 0, 1)
	case "custom":
		var err error
		start, err = time.ParseInLocation("2006-01-02", c.Query("start"), loc)
		if err == nil {
			end, err = time.ParseInLocation("2006-01-02", c.Query("end"), loc)
			end = end.AddDate(0, 0, 1)
		}
		if err != nil || !end.After(start) || end.Sub(start) > 366*24*time.Hour {
			c.JSON(400, gin.H{"error": "Invalid date range"})
			return bounds{}, false
		}
	default:
		c.JSON(400, gin.H{"error": "Unsupported range"})
		return bounds{}, false
	}
	return bounds{start.UTC(), end.UTC(), loc}, true
}
func manager(role cModel.Role) bool { return role == cModel.RoleAdmin || role == cModel.RoleManager }

type workloadRow struct {
	UserID      int    `json:"userId"`
	Name        string `json:"name"`
	Today       int    `json:"today"`
	Overdue     int    `json:"overdue"`
	Upcoming    int    `json:"upcoming"`
	Active      int    `json:"active"`
	Maintenance int    `json:"maintenance"`
}
type categoryRow struct {
	CategoryID *int   `json:"categoryId"`
	Name       string `json:"name"`
	Count      int    `json:"count"`
}
type taskRow struct {
	ID          int        `json:"id"`
	Name        string     `json:"name"`
	AssignedTo  *int       `json:"assignedTo"`
	CategoryID  *int       `json:"categoryId"`
	DueDate     *time.Time `json:"dueDate"`
	Maintenance bool       `json:"maintenance"`
}

func (h *Handler) workload(c *gin.Context) {
	uid, cid, role, loc, ok := h.context(c)
	if !ok {
		return
	}
	b, ok := parseBounds(c, loc, true, h.currentTime())
	if !ok {
		return
	}
	now := h.currentTime()
	today := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc).UTC()
	tomorrow := today.In(loc).AddDate(0, 0, 1).UTC()
	base := h.db.WithContext(c).Table("chores ch").Where("ch.circle_id=? AND ch.is_active=? AND ch.next_due_date IS NOT NULL", cid, true).
		Where("(ch.is_private=? OR ch.created_by=? OR ch.assigned_to=? OR EXISTS (SELECT 1 FROM chore_assignees ca WHERE ca.chore_id=ch.id AND ca.user_id=?))", false, uid, uid, uid)
	if !manager(role) {
		base = base.Where("ch.assigned_to=?", uid)
	}
	if v := c.Query("memberId"); v != "" {
		id, e := strconv.Atoi(v)
		if e != nil || (!manager(role) && id != uid) {
			c.JSON(403, gin.H{"error": "Member filter not permitted"})
			return
		}
		base = base.Where("ch.assigned_to=?", id)
	}
	if v := c.Query("categoryId"); v != "" {
		id, e := strconv.Atoi(v)
		if e != nil {
			c.JSON(400, gin.H{"error": "Invalid category"})
			return
		}
		base = base.Where("ch.category_id=?", id)
	}
	tasks := make([]taskRow, 0)
	q := base.Select("ch.id,ch.name,ch.assigned_to,ch.category_id,ch.next_due_date AS due_date, CASE WHEN tc.chore_id IS NULL THEN false ELSE true END AS maintenance").Joins("LEFT JOIN thing_chores tc ON tc.chore_id=ch.id").Where("ch.next_due_date < ?", b.End).Order("ch.next_due_date, ch.id")
	if err := q.Scan(&tasks).Error; err != nil {
		c.JSON(500, gin.H{"error": "Unable to load workload"})
		return
	}
	members := make([]workloadRow, 0)
	if manager(role) {
		_ = h.db.WithContext(c).Table("user_circles uc").Select("u.id AS user_id, COALESCE(NULLIF(u.display_name,''),u.username) AS name").Joins("JOIN users u ON u.id=uc.user_id").Where("uc.circle_id=? AND uc.is_active=?", cid, true).Order("uc.created_at,uc.user_id").Scan(&members).Error
	} else {
		_ = h.db.WithContext(c).Table("users").Select("id AS user_id, COALESCE(NULLIF(display_name,''),username) AS name").Where("id=?", uid).Scan(&members).Error
	}
	idx := map[int]int{}
	for i := range members {
		idx[members[i].UserID] = i
	}
	unassigned, cats := summarizeWorkload(tasks, members, idx, today, tomorrow, b)
	categories := make([]categoryRow, 0, len(cats))
	for _, v := range cats {
		categories = append(categories, *v)
	}
	var categoryNames []struct {
		ID   int
		Name string
	}
	h.db.WithContext(c).Table("labels").Select("id,name").Where("circle_id=? AND is_category=?", cid, true).Scan(&categoryNames)
	names := make(map[int]string, len(categoryNames))
	for _, row := range categoryNames {
		names[row.ID] = row.Name
	}
	for i := range categories {
		if categories[i].CategoryID != nil {
			categories[i].Name = names[*categories[i].CategoryID]
		}
	}
	c.JSON(200, gin.H{"range": b, "members": members, "unassigned": unassigned, "categories": categories, "tasks": tasks, "canViewFamily": manager(role)})
}

func summarizeWorkload(tasks []taskRow, members []workloadRow, idx map[int]int, today, tomorrow time.Time, b bounds) (int, map[string]*categoryRow) {
	unassigned := 0
	cats := map[string]*categoryRow{}
	for _, t := range tasks {
		if t.AssignedTo == nil {
			unassigned++
		} else if i, yes := idx[*t.AssignedTo]; yes {
			m := &members[i]
			m.Active++
			if t.Maintenance {
				m.Maintenance++
			}
			if t.DueDate.Before(today) {
				m.Overdue++
			} else if t.DueDate.Before(tomorrow) {
				m.Today++
			} else if !t.DueDate.Before(b.Start) && t.DueDate.Before(b.End) {
				m.Upcoming++
			}
		}
		key := "uncategorized"
		name := "Uncategorized"
		if t.CategoryID != nil {
			key = strconv.Itoa(*t.CategoryID)
			name = ""
		}
		if cats[key] == nil {
			cats[key] = &categoryRow{CategoryID: t.CategoryID, Name: name}
		}
		cats[key].Count++
	}
	return unassigned, cats
}

type countName struct {
	ID    *int   `json:"id"`
	Name  string `json:"name"`
	Count int    `json:"count"`
}

func (h *Handler) report(c *gin.Context) {
	uid, cid, role, loc, ok := h.context(c)
	if !ok {
		return
	}
	b, ok := parseBounds(c, loc, false, h.currentTime())
	if !ok {
		return
	}
	hist := h.db.WithContext(c).Table("chore_histories h").Joins("JOIN chores ch ON ch.id=h.chore_id").Where("ch.circle_id=? AND h.status=? AND h.performed_at>=? AND h.performed_at<?", cid, chModel.ChoreHistoryStatusCompleted, b.Start, b.End).
		Where("(ch.is_private=? OR ch.created_by=? OR h.assigned_to=? OR h.completed_by=?)", false, uid, uid, uid)
	if !manager(role) {
		hist = hist.Where("h.completed_by=?", uid)
	}
	var completed int64
	hist.Count(&completed)
	categories := make([]countName, 0)
	hist.Select("ch.category_id AS id, COALESCE(l.name,'Uncategorized') AS name, COUNT(*) AS count").Joins("LEFT JOIN labels l ON l.id=ch.category_id").Group("ch.category_id,l.name").Order("count DESC").Scan(&categories)
	members := make([]countName, 0)
	if manager(role) {
		hist.Select("h.completed_by AS id, COALESCE(NULLIF(u.display_name,''),u.username) AS name, COUNT(*) AS count").Joins("LEFT JOIN users u ON u.id=h.completed_by").Group("h.completed_by,u.display_name,u.username").Order("u.display_name,u.username").Scan(&members)
	}
	var earned, redeemed, available int64
	points := h.db.WithContext(c).Model(&pModel.PointsHistory{}).Where("circle_id=? AND created_at>=? AND created_at<?", cid, b.Start, b.End)
	if !manager(role) {
		points = points.Where("user_id=?", uid)
	}
	points.Where("action=?", pModel.PointsHistoryActionAdd).Select("COALESCE(SUM(points),0)").Scan(&earned)
	points.Where("action=?", pModel.PointsHistoryActionRedeem).Select("COALESCE(SUM(ABS(points)),0)").Scan(&redeemed)
	balances := h.db.WithContext(c).Table("user_circles").Where("circle_id=? AND is_active=?", cid, true)
	if !manager(role) {
		balances = balances.Where("user_id=?", uid)
	}
	balances.Select("COALESCE(SUM(points - points_redeemed - points_adjusted),0)").Scan(&available)
	rewards := make([]struct {
		Status string `json:"status"`
		Count  int    `json:"count"`
	}, 0)
	rewardQuery := h.db.WithContext(c).Table("reward_requests").Select("status,COUNT(*) AS count").Where("circle_id=? AND created_at>=? AND created_at<?", cid, b.Start, b.End)
	if !manager(role) {
		rewardQuery = rewardQuery.Where("user_id=?", uid)
	}
	rewardQuery.Group("status").Scan(&rewards)
	var maintenanceCompleted, maintenanceOverdue, maintenanceApproaching int64
	hist.Joins("JOIN thing_chores tc ON tc.chore_id=ch.id").Count(&maintenanceCompleted)
	now := h.currentTime()
	maintenance := h.db.WithContext(c).Table("chores ch").Joins("JOIN thing_chores tc ON tc.chore_id=ch.id").Where("ch.circle_id=? AND ch.is_active=?", cid, true).
		Where("(ch.is_private=? OR ch.created_by=? OR ch.assigned_to=? OR EXISTS (SELECT 1 FROM chore_assignees ca WHERE ca.chore_id=ch.id AND ca.user_id=?))", false, uid, uid, uid)
	if !manager(role) {
		maintenance = maintenance.Where("ch.assigned_to=?", uid)
	}
	maintenance.Where("ch.next_due_date<?", now).Count(&maintenanceOverdue)
	maintenance.Where("ch.next_due_date>=? AND ch.next_due_date<?", now, now.AddDate(0, 0, 30)).Count(&maintenanceApproaching)
	overdueQuery := h.db.WithContext(c).Table("chores ch").Where("ch.circle_id=? AND ch.is_active=? AND ch.next_due_date<?", cid, true, now).
		Where("(ch.is_private=? OR ch.created_by=? OR ch.assigned_to=? OR EXISTS (SELECT 1 FROM chore_assignees ca WHERE ca.chore_id=ch.id AND ca.user_id=?))", false, uid, uid, uid)
	if !manager(role) {
		overdueQuery = overdueQuery.Where("ch.assigned_to=?", uid)
	}
	var overdue int64
	overdueQuery.Count(&overdue)
	insights := []string{}
	if completed > 0 {
		insights = append(insights, strconv.FormatInt(completed, 10)+" tasks were completed in this period.")
		if len(categories) > 0 {
			insights = append(insights, "Most completed work was in "+categories[0].Name+".")
		}
	} else {
		insights = append(insights, "More activity is needed before trends can be shown.")
	}
	if overdue > 0 {
		insights = append(insights, strconv.FormatInt(overdue, 10)+" tasks are currently overdue.")
	}
	c.JSON(200, gin.H{"range": b, "completed": completed, "overdue": overdue, "categories": categories, "members": members, "points": gin.H{"earned": earned, "redeemed": redeemed, "available": available}, "rewards": rewards, "maintenance": gin.H{"completed": maintenanceCompleted, "overdue": maintenanceOverdue, "approaching": maintenanceApproaching}, "insights": insights, "canViewFamily": manager(role)})
}

func (h *Handler) streak(c *gin.Context) {
	uid, cid, _, loc, ok := h.context(c)
	if !ok {
		return
	}
	now := h.currentTime().In(loc)
	end := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, loc).AddDate(0, 0, 1)
	var rows []struct {
		DueDate     *time.Time
		PerformedAt *time.Time
		Status      int
	}
	h.db.WithContext(c).Table("chore_histories h").Joins("JOIN chores ch ON ch.id=h.chore_id").Select("h.due_date,h.performed_at,h.status").Where("ch.circle_id=? AND (h.assigned_to=? OR h.completed_by=?) AND h.due_date IS NOT NULL AND h.due_date<?", cid, uid, uid, end.UTC()).Order("h.due_date,h.id").Scan(&rows)
	var outstanding []struct {
		DueDate *time.Time
	}
	h.db.WithContext(c).Table("chores ch").Select("ch.next_due_date AS due_date").
		Where("ch.circle_id=? AND ch.is_active=? AND ch.next_due_date IS NOT NULL AND ch.next_due_date<?", cid, true, end.UTC()).
		Where("(ch.assigned_to=? OR EXISTS (SELECT 1 FROM chore_assignees ca WHERE ca.chore_id=ch.id AND ca.user_id=?))", uid, uid).
		Where("(ch.is_private=? OR ch.created_by=? OR ch.assigned_to=? OR EXISTS (SELECT 1 FROM chore_assignees ca WHERE ca.chore_id=ch.id AND ca.user_id=?))", false, uid, uid, uid).
		Scan(&outstanding)
	for _, task := range outstanding {
		rows = append(rows, struct {
			DueDate     *time.Time
			PerformedAt *time.Time
			Status      int
		}{DueDate: task.DueDate, Status: int(chModel.ChoreHistoryStatusMissed)})
	}
	current, best, last := calculateStreak(rows, loc, now)
	c.JSON(200, gin.H{"current": current, "best": best, "lastSuccessfulDay": last, "rule": "Complete at least one assigned task on each day that has expected work; days without expected work are neutral."})
}

func calculateStreak(rows []struct {
	DueDate     *time.Time
	PerformedAt *time.Time
	Status      int
}, loc *time.Location, now time.Time) (int, int, string) {
	type day struct{ expected, success bool }
	days := map[string]*day{}
	var first time.Time
	for _, r := range rows {
		if r.DueDate == nil {
			continue
		}
		k := r.DueDate.In(loc).Format("2006-01-02")
		if days[k] == nil {
			days[k] = &day{}
		}
		days[k].expected = true
		if first.IsZero() || r.DueDate.Before(first) {
			first = *r.DueDate
		}
		if r.Status == int(chModel.ChoreHistoryStatusCompleted) && r.PerformedAt != nil {
			performedKey := r.PerformedAt.In(loc).Format("2006-01-02")
			if days[performedKey] == nil {
				days[performedKey] = &day{}
			}
			days[performedKey].success = true
		}
	}
	if first.IsZero() {
		return 0, 0, ""
	}
	today := time.Date(now.In(loc).Year(), now.In(loc).Month(), now.In(loc).Day(), 0, 0, 0, 0, loc)
	end := today.AddDate(0, 0, 1)
	start := time.Date(first.In(loc).Year(), first.In(loc).Month(), first.In(loc).Day(), 0, 0, 0, 0, loc)
	current, best, run := 0, 0, 0
	var last string
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		v := days[d.Format("2006-01-02")]
		if v == nil || !v.expected {
			continue
		}
		// An unfinished current day is still in progress and must not erase the
		// streak before the user's local day has ended.
		if d.Equal(today) && !v.success {
			continue
		}
		if v.success {
			run++
			last = d.Format("2006-01-02")
			if run > best {
				best = run
			}
		} else {
			run = 0
		}
	}
	current = run
	return current, best, last
}

type goalView struct {
	Goal
	Progress     int    `json:"progress"`
	Percent      int    `json:"percent"`
	CategoryName string `json:"categoryName,omitempty"`
}

func (h *Handler) progress(g Goal) int {
	var n int64
	q := h.db.Table("chore_histories h").Joins("JOIN chores ch ON ch.id=h.chore_id").Where("ch.circle_id=? AND h.status=? AND h.performed_at>=? AND h.performed_at<?", g.CircleID, chModel.ChoreHistoryStatusCompleted, g.StartDate, g.EndDate)
	if g.MetricType == MetricCategoryCompletions {
		q = q.Where("ch.category_id=?", g.CategoryID)
	}
	if g.MetricType == MetricPointsEarned {
		h.db.Model(&pModel.PointsHistory{}).Where("circle_id=? AND action=? AND created_at>=? AND created_at<?", g.CircleID, pModel.PointsHistoryActionAdd, g.StartDate, g.EndDate).Select("COALESCE(SUM(points),0)").Scan(&n)
	} else {
		q.Count(&n)
	}
	return int(n)
}

func (h *Handler) goalProgresses(c *gin.Context, cid int) map[int]int {
	type aggregate struct {
		GoalID int
		Value  int
	}
	var taskRows, pointRows []aggregate
	h.db.WithContext(c).Table("goals g").
		Select("g.id AS goal_id, COUNT(h.id) AS value").
		Joins("JOIN chores ch ON ch.circle_id=g.circle_id").
		Joins("JOIN chore_histories h ON h.chore_id=ch.id AND h.status=? AND h.performed_at>=g.start_date AND h.performed_at<g.end_date", chModel.ChoreHistoryStatusCompleted).
		Where("g.circle_id=? AND (g.metric_type=? OR (g.metric_type=? AND ch.category_id=g.category_id))", cid, MetricTaskCompletions, MetricCategoryCompletions).
		Group("g.id").Scan(&taskRows)
	h.db.WithContext(c).Table("goals g").
		Select("g.id AS goal_id, COALESCE(SUM(ph.points),0) AS value").
		Joins("JOIN points_histories ph ON ph.circle_id=g.circle_id AND ph.action=? AND ph.created_at>=g.start_date AND ph.created_at<g.end_date", pModel.PointsHistoryActionAdd).
		Where("g.circle_id=? AND g.metric_type=?", cid, MetricPointsEarned).
		Group("g.id").Scan(&pointRows)
	result := make(map[int]int, len(taskRows)+len(pointRows))
	for _, row := range append(taskRows, pointRows...) {
		result[row.GoalID] = row.Value
	}
	return result
}

func (h *Handler) goals(c *gin.Context) {
	_, cid, role, _, ok := h.context(c)
	if !ok {
		return
	}
	var goals []Goal
	h.db.WithContext(c).Where("circle_id=?", cid).Order("start_date DESC,id DESC").Find(&goals)
	progresses := h.goalProgresses(c, cid)
	var categoryNames []struct {
		ID   int
		Name string
	}
	h.db.WithContext(c).Table("labels").Select("id,name").Where("circle_id=? AND is_category=?", cid, true).Scan(&categoryNames)
	names := make(map[int]string, len(categoryNames))
	for _, category := range categoryNames {
		names[category.ID] = category.Name
	}
	out := make([]goalView, 0, len(goals))
	for _, g := range goals {
		p := progresses[g.ID]
		if p >= g.Target && g.Status == GoalActive {
			h.db.Model(&Goal{}).Where("id=? AND circle_id=? AND status=?", g.ID, cid, GoalActive).Update("status", GoalCompleted)
			g.Status = GoalCompleted
		}
		pct := 0
		if g.Target > 0 {
			pct = p * 100 / g.Target
			if pct > 100 {
				pct = 100
			}
		}
		v := goalView{Goal: g, Progress: p, Percent: pct}
		if g.CategoryID != nil {
			v.CategoryName = names[*g.CategoryID]
		}
		out = append(out, v)
	}
	c.JSON(200, gin.H{"goals": out, "canManage": manager(role)})
}

type goalInput struct {
	Name       string     `json:"name"`
	MetricType GoalMetric `json:"metricType"`
	Target     int        `json:"target"`
	CategoryID *int       `json:"categoryId"`
	StartDate  string     `json:"startDate"`
	EndDate    string     `json:"endDate"`
	Status     GoalStatus `json:"status"`
}

func (h *Handler) saveGoal(c *gin.Context) {
	uid, cid, role, loc, ok := h.context(c)
	if !ok {
		return
	}
	if !manager(role) {
		c.JSON(403, gin.H{"error": "Manager or admin access required"})
		return
	}
	var in goalInput
	if c.ShouldBindJSON(&in) != nil {
		c.JSON(400, gin.H{"error": "Invalid goal"})
		return
	}
	name := strings.TrimSpace(in.Name)
	start, e1 := time.ParseInLocation("2006-01-02", in.StartDate, loc)
	end, e2 := time.ParseInLocation("2006-01-02", in.EndDate, loc)
	end = end.AddDate(0, 0, 1)
	validMetric := in.MetricType == MetricTaskCompletions || in.MetricType == MetricCategoryCompletions || in.MetricType == MetricPointsEarned
	if name == "" || in.Target < 1 || e1 != nil || e2 != nil || !end.After(start) || !validMetric || (in.MetricType == MetricCategoryCompletions && in.CategoryID == nil) {
		c.JSON(400, gin.H{"error": "Invalid goal definition"})
		return
	}
	if in.CategoryID != nil {
		var n int64
		h.db.Table("labels").Where("id=? AND circle_id=? AND is_category=?", *in.CategoryID, cid, true).Count(&n)
		if n == 0 {
			c.JSON(400, gin.H{"error": "Category not found"})
			return
		}
	}
	g := Goal{CircleID: cid, Name: name, MetricType: in.MetricType, Target: in.Target, CategoryID: in.CategoryID, StartDate: start.UTC(), EndDate: end.UTC(), Status: GoalActive, CreatedBy: uid}
	if id := c.Param("id"); id != "" {
		gid, e := strconv.Atoi(id)
		if e != nil {
			c.JSON(400, gin.H{"error": "Invalid goal"})
			return
		}
		updates := map[string]interface{}{"name": g.Name, "metric_type": g.MetricType, "target": g.Target, "category_id": g.CategoryID, "start_date": g.StartDate, "end_date": g.EndDate}
		if in.Status == GoalArchived {
			updates["status"] = GoalArchived
		}
		var existing Goal
		if h.db.Where("id=? AND circle_id=?", gid, cid).First(&existing).Error != nil {
			c.JSON(404, gin.H{"error": "Goal not found"})
			return
		}
		if existing.Status != GoalActive {
			c.JSON(409, gin.H{"error": "Only active goals can be edited or archived"})
			return
		}
		if err := h.db.Model(&existing).Updates(updates).Error; err != nil {
			c.JSON(500, gin.H{"error": "Unable to update goal"})
			return
		}
		h.db.First(&existing, gid)
		c.JSON(200, gin.H{"goal": existing})
		return
	}
	if h.db.Create(&g).Error != nil {
		c.JSON(500, gin.H{"error": "Unable to create goal"})
		return
	}
	c.JSON(201, gin.H{"goal": g})
}

func Routes(r *gin.Engine, h *Handler, m *auth.MultiAuthMiddleware) {
	g := r.Group("/api/v1/progress")
	g.Use(m.MiddlewareFunc())
	g.GET("/workload", h.workload)
	g.GET("/reports", h.report)
	g.GET("/streak", h.streak)
	g.GET("/goals", h.goals)
	g.POST("/goals", h.saveGoal)
	g.PUT("/goals/:id", h.saveGoal)
}
