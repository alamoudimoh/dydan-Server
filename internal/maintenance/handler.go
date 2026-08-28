package maintenance

import (
	"errors"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/alamoudimoh/daydan-server/internal/auth"
	chModel "github.com/alamoudimoh/daydan-server/internal/chore/model"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/daydan-server/internal/circle/repo"
	mModel "github.com/alamoudimoh/daydan-server/internal/meter/model"
	mRepo "github.com/alamoudimoh/daydan-server/internal/meter/repo"
	tModel "github.com/alamoudimoh/daydan-server/internal/thing/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Handler struct {
	db      *gorm.DB
	circles *cRepo.CircleRepository
}

func NewHandler(db *gorm.DB, circles *cRepo.CircleRepository) *Handler {
	return &Handler{db: db, circles: circles}
}

type assetRequest struct {
	Name        string  `json:"name" binding:"required"`
	Description *string `json:"description"`
	Category    string  `json:"category"`
}

type linkRequest struct {
	MeterID *int `json:"meterId"`
	ChoreID *int `json:"choreId"`
}

type maintenanceItem struct {
	ChoreID       int                   `json:"choreId"`
	AssetID       int                   `json:"assetId"`
	AssetName     string                `json:"assetName"`
	Name          string                `json:"name"`
	NextDueDate   *time.Time            `json:"nextDueDate,omitempty"`
	MeterSchedule *mModel.ChoreSchedule `json:"meterSchedule,omitempty"`
	DueByTime     bool                  `json:"dueByTime"`
	DueReason     string                `json:"dueReason,omitempty"`
	Status        string                `json:"status"`
	LastService   *serviceHistory       `json:"lastService,omitempty"`
}

type serviceHistory struct {
	ID              int        `json:"id"`
	ChoreID         int        `json:"choreId"`
	ChoreName       string     `json:"choreName"`
	PerformedAt     *time.Time `json:"performedAt"`
	CompletedBy     int        `json:"completedBy"`
	CompletedByName string     `json:"completedByName"`
	MeterReading    *int64     `json:"meterReading,omitempty"`
	MeterUnit       string     `json:"meterUnit,omitempty"`
	Points          *int       `json:"points,omitempty"`
	AssetID         int        `json:"-" gorm:"column:asset_id"`
}

type assetDetail struct {
	tModel.Thing
	Meters      []mModel.Meter    `json:"meters"`
	Maintenance []maintenanceItem `json:"maintenance"`
	History     []serviceHistory  `json:"history"`
	Status      string            `json:"status"`
}

func current(c *gin.Context) (int, int, bool) {
	u, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication failed"})
		return 0, 0, false
	}
	return u.ID, u.CircleID, true
}

func idParam(c *gin.Context) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id <= 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid asset ID"})
		return 0, false
	}
	return id, true
}

func (h *Handler) canManage(c *gin.Context, userID, circleID int) bool {
	role, err := h.circles.GetActiveUserCircleRole(c, circleID, userID)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Manager or admin access required"})
		return false
	}
	// AUD-07: Cap managed-child accounts to member regardless of stale role.
	if u, ok := auth.CurrentUser(c); ok {
		role = auth.CapEffectiveCircleRole(&u.User, role)
	}
	if role != cModel.RoleAdmin && role != cModel.RoleManager {
		c.JSON(http.StatusForbidden, gin.H{"error": "Manager or admin access required"})
		return false
	}
	return true
}

func visibleAssetScope(db *gorm.DB, userID int) *gorm.DB {
	return db.Where(`(
		things.is_shared = ? OR things.user_id = ? OR EXISTS (
			SELECT 1
			FROM thing_chores tc
			JOIN chores ON chores.id = tc.chore_id
			JOIN chore_assignees ON chore_assignees.chore_id = chores.id
			WHERE tc.thing_id = things.id
			  AND chores.is_private = true
			  AND chore_assignees.user_id = ?
		)
	)`, true, userID, userID)
}

func (h *Handler) asset(c *gin.Context, id, circleID, userID int) (*tModel.Thing, error) {
	var asset tModel.Thing
	err := visibleAssetScope(h.db.WithContext(c), userID).Where("things.id = ? AND things.circle_id = ?", id, circleID).First(&asset).Error
	return &asset, err
}

func (h *Handler) Create(c *gin.Context) {
	userID, circleID, ok := current(c)
	if !ok || !h.canManage(c, userID, circleID) {
		return
	}
	var req assetRequest
	if c.ShouldBindJSON(&req) != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(400, gin.H{"error": "Invalid asset"})
		return
	}
	now := time.Now().UTC()
	asset := tModel.Thing{UserID: userID, CircleID: circleID, IsShared: true, Name: strings.TrimSpace(req.Name), Description: req.Description, Category: strings.TrimSpace(req.Category), Type: string(tModel.ThingTypeText), CreatedAt: &now, UpdatedAt: &now}
	if err := h.db.WithContext(c).Create(&asset).Error; err != nil {
		c.JSON(500, gin.H{"error": "Failed to create asset"})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"res": asset})
}

func (h *Handler) List(c *gin.Context) {
	userID, circleID, ok := current(c)
	if !ok {
		return
	}
	includeArchived := c.Query("includeArchived") == "true"
	var assets []tModel.Thing
	q := visibleAssetScope(h.db.WithContext(c), userID).Where("things.circle_id = ?", circleID)
	if !includeArchived {
		q = q.Where("archived_at IS NULL")
	}
	if err := q.Order("name").Find(&assets).Error; err != nil {
		c.JSON(500, gin.H{"error": "Failed to retrieve assets"})
		return
	}
	c.JSON(200, gin.H{"res": assets})
}

func (h *Handler) Update(c *gin.Context) {
	userID, circleID, ok := current(c)
	if !ok || !h.canManage(c, userID, circleID) {
		return
	}
	id, ok := idParam(c)
	if !ok {
		return
	}
	if _, err := h.asset(c, id, circleID, userID); err != nil {
		c.JSON(404, gin.H{"error": "Asset not found"})
		return
	}
	var req assetRequest
	if c.ShouldBindJSON(&req) != nil || strings.TrimSpace(req.Name) == "" {
		c.JSON(400, gin.H{"error": "Invalid asset"})
		return
	}
	result := visibleAssetScope(h.db.WithContext(c).Model(&tModel.Thing{}), userID).Where("things.id = ? AND things.circle_id = ?", id, circleID).Updates(map[string]interface{}{"name": strings.TrimSpace(req.Name), "description": req.Description, "category": strings.TrimSpace(req.Category), "updated_at": time.Now().UTC()})
	if result.Error != nil {
		c.JSON(500, gin.H{"error": "Failed to update asset"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(404, gin.H{"error": "Asset not found"})
		return
	}
	asset, _ := h.asset(c, id, circleID, userID)
	c.JSON(200, gin.H{"res": asset})
}

func (h *Handler) Archive(c *gin.Context) {
	userID, circleID, ok := current(c)
	if !ok || !h.canManage(c, userID, circleID) {
		return
	}
	id, ok := idParam(c)
	if !ok {
		return
	}
	if _, err := h.asset(c, id, circleID, userID); err != nil {
		c.JSON(404, gin.H{"error": "Asset not found"})
		return
	}
	result := visibleAssetScope(h.db.WithContext(c).Model(&tModel.Thing{}), userID).Where("things.id = ? AND things.circle_id = ? AND things.archived_at IS NULL", id, circleID).Update("archived_at", time.Now().UTC())
	if result.Error != nil {
		c.JSON(500, gin.H{"error": "Failed to archive asset"})
		return
	}
	if result.RowsAffected == 0 {
		c.JSON(404, gin.H{"error": "Asset not found"})
		return
	}
	c.Status(204)
	c.Writer.WriteHeaderNow()
}

func (h *Handler) Link(c *gin.Context) {
	userID, circleID, ok := current(c)
	if !ok || !h.canManage(c, userID, circleID) {
		return
	}
	id, ok := idParam(c)
	if !ok {
		return
	}
	var req linkRequest
	if c.ShouldBindJSON(&req) != nil || (req.MeterID == nil && req.ChoreID == nil) || (req.MeterID != nil && req.ChoreID != nil) {
		c.JSON(400, gin.H{"error": "Specify one meterId or choreId"})
		return
	}
	linkErr := h.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		var asset tModel.Thing
		if err := visibleAssetScope(tx, userID).Where("things.id = ? AND things.circle_id = ?", id, circleID).First(&asset).Error; err != nil {
			return err
		}
		if req.MeterID != nil {
			result := tx.Model(&mModel.Meter{}).Where("id = ? AND circle_id = ?", *req.MeterID, circleID).Update("thing_id", id)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				return gorm.ErrRecordNotFound
			}
			return nil
		}

		var chore chModel.Chore
		if err := tx.Model(&chModel.Chore{}).Joins("LEFT JOIN chore_assignees ON chores.id = chore_assignees.chore_id AND chore_assignees.user_id = ?", userID).
			Where("chores.id = ? AND chores.circle_id = ?", *req.ChoreID, circleID).
			Where("((chores.is_private = false) OR (chores.is_private = true AND (chores.created_by = ? OR chore_assignees.user_id = ?)))", userID, userID).
			First(&chore).Error; err != nil {
			return err
		}
		var existing tModel.ThingChore
		if err := tx.Where("chore_id = ?", *req.ChoreID).First(&existing).Error; err == nil {
			return tx.Model(&tModel.ThingChore{}).Where("thing_id = ? AND chore_id = ?", existing.ThingID, existing.ChoreID).Update("thing_id", id).Error
		} else if errors.Is(err, gorm.ErrRecordNotFound) {
			return tx.Create(&tModel.ThingChore{ThingID: id, ChoreID: *req.ChoreID}).Error
		} else {
			return err
		}
	})
	if linkErr != nil {
		if errors.Is(linkErr, gorm.ErrRecordNotFound) {
			c.JSON(404, gin.H{"error": "Asset or link target not found"})
			return
		}
		c.JSON(500, gin.H{"error": "Failed to link asset"})
		return
	}
	c.Status(204)
	c.Writer.WriteHeaderNow()
}

func (h *Handler) Detail(c *gin.Context) {
	userID, circleID, ok := current(c)
	if !ok {
		return
	}
	id, ok := idParam(c)
	if !ok {
		return
	}
	asset, err := h.asset(c, id, circleID, userID)
	if err != nil {
		c.JSON(404, gin.H{"error": "Asset not found"})
		return
	}
	detail, err := h.buildDetail(c, *asset, circleID, userID)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to retrieve asset"})
		return
	}
	c.JSON(200, gin.H{"res": detail})
}

func (h *Handler) buildDetail(c *gin.Context, asset tModel.Thing, circleID int, userID int) (assetDetail, error) {
	d := assetDetail{Thing: asset, Meters: []mModel.Meter{}, Maintenance: []maintenanceItem{}, History: []serviceHistory{}, Status: "all_good"}
	allMeters, err := mRepo.NewRepository(h.db).List(c, circleID)
	if err != nil {
		return d, err
	}
	for _, meter := range allMeters {
		if meter.ThingID != nil && *meter.ThingID == asset.ID {
			d.Meters = append(d.Meters, meter)
		}
	}
	var chores []chModel.Chore
	// AUD-06: Apply chore privacy predicate so private chores are only visible
	// to their creator or assignees, matching GetChores/GetChore repository queries.
	if err := h.db.WithContext(c).
		Joins("JOIN thing_chores tc ON tc.chore_id = chores.id").
		Joins("LEFT JOIN chore_assignees ON chores.id = chore_assignees.chore_id AND chore_assignees.user_id = ?", userID).
		Where("tc.thing_id = ? AND chores.circle_id = ? AND chores.is_active = ?", asset.ID, circleID, true).
		Where("((chores.is_private = false) OR (chores.is_private = true AND (chores.created_by = ? OR chore_assignees.user_id = ?)))", userID, userID).
		Preload("MeterSchedule").
		Find(&chores).Error; err != nil {
		return d, err
	}
	now := time.Now().UTC()
	hasUpcoming := false
	for _, chore := range chores {
		item := maintenanceItem{ChoreID: chore.ID, AssetID: asset.ID, AssetName: asset.Name, Name: chore.Name, NextDueDate: chore.NextDueDate, MeterSchedule: chore.MeterSchedule, Status: "upcoming"}
		item.DueByTime = chore.NextDueDate != nil && !chore.NextDueDate.After(now)
		if item.MeterSchedule != nil {
			// The chore's NextDueDate may be pulled forward to "now" when the
			// meter wins. Preserve the independent calendar condition for hybrid UI.
			item.NextDueDate = item.MeterSchedule.CalendarDueDate
			var meter mModel.Meter
			if h.db.WithContext(c).Where("id = ? AND circle_id = ?", item.MeterSchedule.MeterID, circleID).First(&meter).Error == nil {
				mRepo.PopulateScheduleAt(item.MeterSchedule, meter, now)
				item.DueByTime = item.MeterSchedule.DueByTime
				item.DueReason = item.MeterSchedule.DueReason
			}
		}
		if item.MeterSchedule == nil && item.DueByTime {
			item.DueReason = "time"
		}
		if item.DueReason != "" {
			item.Status = "due"
			d.Status = "attention"
		} else if item.MeterSchedule != nil && item.MeterSchedule.ApproachingByMeter {
			item.Status = "approaching"
			d.Status = "approaching"
			hasUpcoming = true
		} else {
			hasUpcoming = true
		}
		var last serviceHistory
		q := h.historyQuery(c, asset.ID, circleID, userID).Where("chore_histories.chore_id = ?", chore.ID).Limit(1)
		if q.Scan(&last).Error == nil && last.ID != 0 {
			item.LastService = &last
		}
		d.Maintenance = append(d.Maintenance, item)
	}
	if d.Status != "attention" && hasUpcoming {
		d.Status = "upcoming"
	}
	if err := h.historyQuery(c, asset.ID, circleID, userID).Limit(50).Scan(&d.History).Error; err != nil {
		return d, err
	}
	sort.SliceStable(d.Maintenance, func(i, j int) bool {
		if d.Maintenance[i].Status != d.Maintenance[j].Status {
			return d.Maintenance[i].Status == "due"
		}
		a, b := d.Maintenance[i].NextDueDate, d.Maintenance[j].NextDueDate
		if a == nil {
			return false
		}
		if b == nil {
			return true
		}
		return a.Before(*b)
	})
	return d, nil
}

func (h *Handler) historyQuery(c *gin.Context, assetID, circleID, userID int) *gorm.DB {
	return h.db.WithContext(c).Table("chore_histories").
		Select("chore_histories.id, chore_histories.chore_id, chores.name AS chore_name, chore_histories.performed_at, chore_histories.completed_by, COALESCE(users.display_name, users.username) AS completed_by_name, chore_histories.meter_reading, meters.unit AS meter_unit, chore_histories.points").
		Joins("JOIN chores ON chores.id = chore_histories.chore_id").
		Joins("JOIN thing_chores tc ON tc.chore_id = chores.id").
		Joins("LEFT JOIN chore_assignees ON chores.id = chore_assignees.chore_id AND chore_assignees.user_id = ?", userID).
		Joins("LEFT JOIN users ON users.id = chore_histories.completed_by").
		Joins("LEFT JOIN meters ON meters.id = chore_histories.meter_id").
		Where("tc.thing_id = ? AND chores.circle_id = ? AND chore_histories.status = ?", assetID, circleID, chModel.ChoreHistoryStatusCompleted).
		Where("((chores.is_private = false) OR (chores.is_private = true AND (chores.created_by = ? OR chore_assignees.user_id = ?)))", userID, userID).
		Order("chore_histories.performed_at DESC, chore_histories.id DESC")
}

func (h *Handler) Center(c *gin.Context) {
	userID, circleID, ok := current(c)
	if !ok {
		return
	}
	var assets []tModel.Thing
	if visibleAssetScope(h.db.WithContext(c), userID).Where("things.circle_id = ? AND things.archived_at IS NULL", circleID).Order("things.name").Find(&assets).Error != nil {
		c.JSON(500, gin.H{"error": "Failed to retrieve maintenance"})
		return
	}
	details := make([]assetDetail, 0, len(assets))
	due := []maintenanceItem{}
	approaching := []maintenanceItem{}
	upcoming := []maintenanceItem{}
	assetIndex := make(map[int]int, len(assets))
	for _, asset := range assets {
		assetIndex[asset.ID] = len(details)
		details = append(details, assetDetail{Thing: asset, Meters: []mModel.Meter{}, Maintenance: []maintenanceItem{}, History: []serviceHistory{}, Status: "all_good"})
	}
	meters, err := mRepo.NewRepository(h.db).List(c, circleID)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to retrieve maintenance"})
		return
	}
	meterByID := make(map[int]mModel.Meter, len(meters))
	for _, meter := range meters {
		meterByID[meter.ID] = meter
		if meter.ThingID != nil {
			if index, found := assetIndex[*meter.ThingID]; found {
				details[index].Meters = append(details[index].Meters, meter)
			}
		}
	}
	var chores []chModel.Chore
	// AUD-06: Apply chore privacy predicate so private chores are only visible
	// to their creator or assignees.
	if err := h.db.WithContext(c).
		Joins("JOIN thing_chores tc ON tc.chore_id = chores.id").
		Joins("LEFT JOIN chore_assignees ON chores.id = chore_assignees.chore_id AND chore_assignees.user_id = ?", userID).
		Where("chores.circle_id = ? AND chores.is_active = ?", circleID, true).
		Where("((chores.is_private = false) OR (chores.is_private = true AND (chores.created_by = ? OR chore_assignees.user_id = ?)))", userID, userID).
		Preload("ThingChore").Preload("MeterSchedule").Find(&chores).Error; err != nil {
		c.JSON(500, gin.H{"error": "Failed to retrieve maintenance"})
		return
	}
	var history []serviceHistory
	if err := h.db.WithContext(c).Table("chore_histories").
		Select("chore_histories.id, chore_histories.chore_id, chores.name AS chore_name, tc.thing_id AS asset_id, chore_histories.performed_at, chore_histories.completed_by, COALESCE(users.display_name, users.username) AS completed_by_name, chore_histories.meter_reading, meters.unit AS meter_unit, chore_histories.points").
		Joins("JOIN chores ON chores.id = chore_histories.chore_id").
		Joins("JOIN thing_chores tc ON tc.chore_id = chores.id").
		Joins("LEFT JOIN chore_assignees ON chores.id = chore_assignees.chore_id AND chore_assignees.user_id = ?", userID).
		Joins("LEFT JOIN users ON users.id = chore_histories.completed_by").
		Joins("LEFT JOIN meters ON meters.id = chore_histories.meter_id").
		Where("chores.circle_id = ? AND chore_histories.status = ?", circleID, chModel.ChoreHistoryStatusCompleted).
		Where("((chores.is_private = false) OR (chores.is_private = true AND (chores.created_by = ? OR chore_assignees.user_id = ?)))", userID, userID).
		Order("chore_histories.performed_at DESC, chore_histories.id DESC").Limit(200).Scan(&history).Error; err != nil {
		c.JSON(500, gin.H{"error": "Failed to retrieve maintenance"})
		return
	}
	lastByChore := make(map[int]serviceHistory)
	for _, entry := range history {
		if _, exists := lastByChore[entry.ChoreID]; !exists {
			lastByChore[entry.ChoreID] = entry
		}
		if index, found := assetIndex[entry.AssetID]; found {
			details[index].History = append(details[index].History, entry)
		}
	}
	now := time.Now().UTC()
	for _, chore := range chores {
		if chore.ThingChore == nil {
			continue
		}
		index, found := assetIndex[chore.ThingChore.ThingID]
		if !found {
			continue
		}
		asset := details[index].Thing
		item := maintenanceItem{ChoreID: chore.ID, AssetID: asset.ID, AssetName: asset.Name, Name: chore.Name, NextDueDate: chore.NextDueDate, MeterSchedule: chore.MeterSchedule, Status: "upcoming"}
		item.DueByTime = chore.NextDueDate != nil && !chore.NextDueDate.After(now)
		if item.MeterSchedule != nil {
			item.NextDueDate = item.MeterSchedule.CalendarDueDate
			if meter, exists := meterByID[item.MeterSchedule.MeterID]; exists {
				mRepo.PopulateScheduleAt(item.MeterSchedule, meter, now)
				item.DueByTime = item.MeterSchedule.DueByTime
				item.DueReason = item.MeterSchedule.DueReason
			}
		} else if item.DueByTime {
			item.DueReason = "time"
		}
		if last, exists := lastByChore[chore.ID]; exists {
			copy := last
			item.LastService = &copy
		}
		if item.DueReason != "" {
			item.Status = "due"
			details[index].Status = "attention"
			due = append(due, item)
		} else if item.MeterSchedule != nil && item.MeterSchedule.ApproachingByMeter {
			item.Status = "approaching"
			if details[index].Status != "attention" {
				details[index].Status = "approaching"
			}
			approaching = append(approaching, item)
		} else {
			if details[index].Status == "all_good" {
				details[index].Status = "upcoming"
			}
			upcoming = append(upcoming, item)
		}
		details[index].Maintenance = append(details[index].Maintenance, item)
	}
	sort.SliceStable(due, func(i, j int) bool {
		return due[i].AssetName+due[i].Name < due[j].AssetName+due[j].Name
	})
	sort.SliceStable(upcoming, func(i, j int) bool {
		a, b := upcoming[i].NextDueDate, upcoming[j].NextDueDate
		if a == nil && b == nil {
			return upcoming[i].AssetName+upcoming[i].Name < upcoming[j].AssetName+upcoming[j].Name
		}
		if a == nil {
			return false
		}
		if b == nil {
			return true
		}
		if a.Equal(*b) {
			return upcoming[i].AssetName+upcoming[i].Name < upcoming[j].AssetName+upcoming[j].Name
		}
		return a.Before(*b)
	})
	c.JSON(200, gin.H{"res": gin.H{"due": due, "approaching": approaching, "upcoming": upcoming, "assets": details}})
}

func Routes(router *gin.Engine, h *Handler, middleware *auth.MultiAuthMiddleware) {
	r := router.Group("api/v1")
	r.Use(middleware.MiddlewareFunc())
	r.GET("/maintenance/assets", h.List)
	r.POST("/maintenance/assets", h.Create)
	r.GET("/maintenance/assets/:id", h.Detail)
	r.PUT("/maintenance/assets/:id", h.Update)
	r.DELETE("/maintenance/assets/:id", h.Archive)
	r.POST("/maintenance/assets/:id/links", h.Link)
	r.GET("/maintenance", h.Center)
}
