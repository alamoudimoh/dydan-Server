package routine

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"donetick.com/core/internal/auth"
	"donetick.com/core/internal/chore"
	chModel "donetick.com/core/internal/chore/model"
	chRepo "donetick.com/core/internal/chore/repo"
	cModel "donetick.com/core/internal/circle/model"
	cRepo "donetick.com/core/internal/circle/repo"
	lModel "donetick.com/core/internal/label/model"
	mModel "donetick.com/core/internal/meter/model"
	tModel "donetick.com/core/internal/thing/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Handler struct {
	db      *gorm.DB
	chores  *chRepo.ChoreRepository
	circles *cRepo.CircleRepository
}

func NewHandler(db *gorm.DB, chores *chRepo.ChoreRepository, circles *cRepo.CircleRepository) *Handler {
	return &Handler{db: db, chores: chores, circles: circles}
}

func current(c *gin.Context) (int, int, bool) {
	u, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return 0, 0, false
	}
	return u.ID, u.CircleID, true
}

func (h *Handler) role(c *gin.Context, circleID, userID int) (cModel.Role, bool) {
	role, err := h.circles.GetActiveUserCircleRole(c, circleID, userID)
	if err != nil {
		c.JSON(http.StatusForbidden, gin.H{"error": "Active Circle membership required"})
		return "", false
	}
	return role, true
}

func id(c *gin.Context, name string) (int, bool) {
	v, err := strconv.Atoi(c.Param(name))
	if err != nil || v < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID"})
		return 0, false
	}
	return v, true
}

func (h *Handler) pack(c *gin.Context, packID, circleID int) (*Pack, error) {
	var pack Pack
	err := h.db.WithContext(c).Preload("Items", func(db *gorm.DB) *gorm.DB { return db.Order("position ASC, id ASC") }).Where("id = ? AND circle_id = ?", packID, circleID).First(&pack).Error
	return &pack, err
}

func canManage(pack *Pack, userID int, role cModel.Role) bool {
	return pack.CreatedBy == userID || role == cModel.RoleAdmin || role == cModel.RoleManager
}

func (h *Handler) List(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok {
		return
	}
	if _, ok := h.role(c, cid, uid); !ok {
		return
	}
	var packs []Pack
	q := h.db.WithContext(c).Preload("Items", func(db *gorm.DB) *gorm.DB { return db.Order("position ASC, id ASC") }).Where("circle_id = ?", cid)
	if c.Query("includeArchived") != "true" {
		q = q.Where("is_active = ?", true)
	}
	if err := q.Order("is_active DESC, updated_at DESC, id DESC").Find(&packs).Error; err != nil {
		c.JSON(500, gin.H{"error": "Unable to list routines"})
		return
	}
	c.JSON(200, gin.H{"res": packs})
}

func (h *Handler) Read(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok {
		return
	}
	if _, ok := h.role(c, cid, uid); !ok {
		return
	}
	packID, ok := id(c, "id")
	if !ok {
		return
	}
	pack, err := h.pack(c, packID, cid)
	if err != nil {
		c.JSON(404, gin.H{"error": "Routine not found"})
		return
	}
	c.JSON(200, gin.H{"res": pack})
}

type packInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
}

func bindPack(c *gin.Context) (packInput, bool) {
	var input packInput
	if c.ShouldBindJSON(&input) != nil {
		c.JSON(400, gin.H{"error": "Invalid routine"})
		return input, false
	}
	input.Name = strings.TrimSpace(input.Name)
	input.Description = strings.TrimSpace(input.Description)
	if input.Name == "" || len(input.Name) > 120 || len(input.Description) > 2000 {
		c.JSON(400, gin.H{"error": "Routine name is required"})
		return input, false
	}
	return input, true
}

func (h *Handler) Create(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok {
		return
	}
	if _, ok := h.role(c, cid, uid); !ok {
		return
	}
	input, ok := bindPack(c)
	if !ok {
		return
	}
	now := time.Now().UTC()
	pack := Pack{CircleID: cid, Name: input.Name, Description: input.Description, IsActive: true, CreatedBy: uid, CreatedAt: now, UpdatedAt: now}
	if err := h.db.WithContext(c).Create(&pack).Error; err != nil {
		c.JSON(500, gin.H{"error": "Unable to create routine"})
		return
	}
	c.JSON(201, gin.H{"res": pack})
}

func (h *Handler) Update(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok {
		return
	}
	role, ok := h.role(c, cid, uid)
	if !ok {
		return
	}
	packID, ok := id(c, "id")
	if !ok {
		return
	}
	pack, err := h.pack(c, packID, cid)
	if err != nil {
		c.JSON(404, gin.H{"error": "Routine not found"})
		return
	}
	if !canManage(pack, uid, role) {
		c.JSON(403, gin.H{"error": "Routine owner or manager permission required"})
		return
	}
	input, ok := bindPack(c)
	if !ok {
		return
	}
	if err := h.db.WithContext(c).Model(&Pack{}).Where("id = ? AND circle_id = ?", packID, cid).Updates(map[string]any{"name": input.Name, "description": input.Description, "updated_at": time.Now().UTC()}).Error; err != nil {
		c.JSON(500, gin.H{"error": "Unable to update routine"})
		return
	}
	pack, _ = h.pack(c, packID, cid)
	c.JSON(200, gin.H{"res": pack})
}

func (h *Handler) SetActive(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok {
		return
	}
	role, ok := h.role(c, cid, uid)
	if !ok {
		return
	}
	packID, ok := id(c, "id")
	if !ok {
		return
	}
	pack, err := h.pack(c, packID, cid)
	if err != nil {
		c.JSON(404, gin.H{"error": "Routine not found"})
		return
	}
	if !canManage(pack, uid, role) {
		c.JSON(403, gin.H{"error": "Routine owner or manager permission required"})
		return
	}
	var input struct {
		IsActive *bool `json:"isActive"`
	}
	if c.ShouldBindJSON(&input) != nil || input.IsActive == nil {
		c.JSON(400, gin.H{"error": "isActive is required"})
		return
	}
	if err := h.db.WithContext(c).Model(&Pack{}).Where("id = ? AND circle_id = ?", packID, cid).Updates(map[string]any{"is_active": *input.IsActive, "updated_at": time.Now().UTC()}).Error; err != nil {
		c.JSON(500, gin.H{"error": "Unable to update routine"})
		return
	}
	pack.IsActive = *input.IsActive
	c.JSON(200, gin.H{"res": pack})
}

func normalizeItem(item *Item) error {
	item.Name = strings.TrimSpace(item.Name)
	if item.Name == "" || len(item.Name) > 200 {
		return errors.New("task name is required")
	}
	if item.FrequencyType == "" {
		item.FrequencyType = chModel.FrequencyTypeOnce
	}
	if item.Frequency < 1 {
		item.Frequency = 1
	}
	if item.AssignStrategy == "" {
		item.AssignStrategy = chModel.AssignmentStrategyNoAssignee
	}
	validFrequency := map[chModel.FrequencyType]bool{
		chModel.FrequencyTypeOnce: true, chModel.FrequencyTypeDaily: true,
		chModel.FrequencyTypeWeekly: true, chModel.FrequencyTypeMonthly: true,
		chModel.FrequencyTypeYearly: true, chModel.FrequencyTypeAdaptive: true,
		chModel.FrequencyTypeInterval: true, chModel.FrequencyTypeDayOfTheWeek: true,
		chModel.FrequencyTypeDayOfTheMonth: true, chModel.FrequencyTypeTrigger: true,
		chModel.FrequencyTypeNoRepeat: true,
	}
	if !validFrequency[item.FrequencyType] {
		return errors.New("unsupported recurrence type")
	}
	validStrategy := map[chModel.AssignmentStrategy]bool{
		chModel.AssignmentStrategyNoAssignee: true, chModel.AssignmentStrategyRandom: true,
		chModel.AssignmentStrategyLeastAssigned: true, chModel.AssignmentStrategyLeastCompleted: true,
		chModel.AssignmentStrategyKeepLastAssigned:         true,
		chModel.AssignmentStrategyRandomExceptLastAssigned: true,
		chModel.AssignmentStrategyRoundRobin:               true,
	}
	if !validStrategy[item.AssignStrategy] {
		return errors.New("unsupported assignment strategy")
	}
	if item.MeterID == nil {
		item.MeterInterval = nil
	} else if item.MeterInterval == nil || *item.MeterInterval < 1 {
		return errors.New("meter interval must be positive")
	}
	return nil
}

func (h *Handler) managedPack(c *gin.Context, uid, cid, packID int) (*Pack, bool) {
	role, ok := h.role(c, cid, uid)
	if !ok {
		return nil, false
	}
	pack, err := h.pack(c, packID, cid)
	if err != nil {
		c.JSON(404, gin.H{"error": "Routine not found"})
		return nil, false
	}
	if !canManage(pack, uid, role) {
		c.JSON(403, gin.H{"error": "Routine owner or manager permission required"})
		return nil, false
	}
	return pack, true
}

func (h *Handler) AddItem(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok {
		return
	}
	packID, ok := id(c, "id")
	if !ok {
		return
	}
	pack, ok := h.managedPack(c, uid, cid, packID)
	if !ok {
		return
	}
	var item Item
	if c.ShouldBindJSON(&item) != nil || normalizeItem(&item) != nil {
		c.JSON(400, gin.H{"error": "Invalid routine task"})
		return
	}
	item.ID = 0
	item.PackID = pack.ID
	item.Position = len(pack.Items)
	if err := h.validateReferences(c, cid, &item, nil); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.WithContext(c).Create(&item).Error; err != nil {
		c.JSON(500, gin.H{"error": "Unable to add routine task"})
		return
	}
	h.db.WithContext(c).Model(&Pack{}).Where("id = ?", packID).Update("updated_at", time.Now().UTC())
	c.JSON(201, gin.H{"res": item})
}

func (h *Handler) UpdateItem(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok {
		return
	}
	packID, ok := id(c, "id")
	if !ok {
		return
	}
	itemID, ok := id(c, "itemId")
	if !ok {
		return
	}
	if _, ok := h.managedPack(c, uid, cid, packID); !ok {
		return
	}
	var item Item
	if c.ShouldBindJSON(&item) != nil || normalizeItem(&item) != nil {
		c.JSON(400, gin.H{"error": "Invalid routine task"})
		return
	}
	var existing Item
	if err := h.db.WithContext(c).Where("id = ? AND pack_id = ?", itemID, packID).First(&existing).Error; err != nil {
		c.JSON(404, gin.H{"error": "Routine task not found"})
		return
	}
	item.ID, item.PackID, item.Position, item.CreatedAt = existing.ID, existing.PackID, existing.Position, existing.CreatedAt
	if err := h.validateReferences(c, cid, &item, nil); err != nil {
		c.JSON(400, gin.H{"error": err.Error()})
		return
	}
	if err := h.db.WithContext(c).Save(&item).Error; err != nil {
		c.JSON(500, gin.H{"error": "Unable to update routine task"})
		return
	}
	c.JSON(200, gin.H{"res": item})
}

func (h *Handler) DeleteItem(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok {
		return
	}
	packID, ok := id(c, "id")
	if !ok {
		return
	}
	itemID, ok := id(c, "itemId")
	if !ok {
		return
	}
	if _, ok := h.managedPack(c, uid, cid, packID); !ok {
		return
	}
	res := h.db.WithContext(c).Where("id = ? AND pack_id = ?", itemID, packID).Delete(&Item{})
	if res.Error != nil || res.RowsAffected != 1 {
		c.JSON(404, gin.H{"error": "Routine task not found"})
		return
	}
	h.resequence(c, packID)
	c.Status(204)
}

func (h *Handler) Reorder(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok {
		return
	}
	packID, ok := id(c, "id")
	if !ok {
		return
	}
	pack, ok := h.managedPack(c, uid, cid, packID)
	if !ok {
		return
	}
	var input struct {
		ItemIDs []int `json:"itemIds"`
	}
	if c.ShouldBindJSON(&input) != nil || len(input.ItemIDs) != len(pack.Items) {
		c.JSON(400, gin.H{"error": "Complete task order is required"})
		return
	}
	expected := map[int]bool{}
	for _, item := range pack.Items {
		expected[item.ID] = true
	}
	for _, itemID := range input.ItemIDs {
		if !expected[itemID] {
			c.JSON(400, gin.H{"error": "Task order contains an invalid item"})
			return
		}
		delete(expected, itemID)
	}
	if len(expected) != 0 {
		c.JSON(400, gin.H{"error": "Task order contains duplicates"})
		return
	}
	err := h.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		for position, itemID := range input.ItemIDs {
			if err := tx.Model(&Item{}).Where("id = ? AND pack_id = ?", itemID, packID).Update("position", position).Error; err != nil {
				return err
			}
		}
		return tx.Model(&Pack{}).Where("id = ?", packID).Update("updated_at", time.Now().UTC()).Error
	})
	if err != nil {
		c.JSON(500, gin.H{"error": "Unable to reorder tasks"})
		return
	}
	pack, _ = h.pack(c, packID, cid)
	c.JSON(200, gin.H{"res": pack})
}

func (h *Handler) resequence(c *gin.Context, packID int) {
	var items []Item
	if h.db.WithContext(c).Where("pack_id = ?", packID).Order("position, id").Find(&items).Error == nil {
		for position := range items {
			h.db.WithContext(c).Model(&items[position]).Update("position", position)
		}
	}
}

type applyInput struct {
	StartDate       string      `json:"startDate"`
	IdempotencyKey  string      `json:"idempotencyKey"`
	AssetSelections map[int]int `json:"assetSelections"`
}

func parseStart(value string) (time.Time, error) {
	if value == "" {
		return time.Now().UTC(), nil
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC(), nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	return parsed.UTC(), err
}

func (h *Handler) Apply(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok {
		return
	}
	if _, ok := h.role(c, cid, uid); !ok {
		return
	}
	packID, ok := id(c, "id")
	if !ok {
		return
	}
	pack, err := h.pack(c, packID, cid)
	if err != nil {
		c.JSON(404, gin.H{"error": "Routine not found"})
		return
	}
	if !pack.IsActive {
		c.JSON(409, gin.H{"error": "Archived routines cannot be used"})
		return
	}
	if len(pack.Items) == 0 {
		c.JSON(400, gin.H{"error": "Routine has no tasks"})
		return
	}
	var input applyInput
	if c.ShouldBindJSON(&input) != nil || len(strings.TrimSpace(input.IdempotencyKey)) < 8 || len(input.IdempotencyKey) > 100 {
		c.JSON(400, gin.H{"error": "Start date and idempotency key are required"})
		return
	}
	start, err := parseStart(input.StartDate)
	if err != nil {
		c.JSON(400, gin.H{"error": "Invalid start date"})
		return
	}
	var previous Application
	if h.db.WithContext(c).Where("circle_id = ? AND idempotency_key = ?", cid, input.IdempotencyKey).First(&previous).Error == nil {
		c.JSON(200, gin.H{"res": previous, "replayed": true})
		return
	}
	reservation := Application{CircleID: cid, PackID: pack.ID, IdempotencyKey: input.IdempotencyKey, AppliedBy: uid, AppliedAt: time.Now().UTC()}
	specs := make([]chRepo.CreateSpec, 0, len(pack.Items))
	for index := range pack.Items {
		item := &pack.Items[index]
		selection := input.AssetSelections[item.ID]
		if err := h.validateReferences(c, cid, item, &selection); err != nil {
			c.JSON(400, gin.H{"error": err.Error(), "itemId": item.ID})
			return
		}
		assetID := item.AssetID
		if selection > 0 {
			assetID = &selection
		}
		choreModel := &chModel.Chore{Name: item.Name, Description: item.Description, FrequencyType: item.FrequencyType, Frequency: item.Frequency, FrequencyMetadataV2: item.FrequencyMetadata, IsRolling: item.IsRolling, AssignedTo: item.AssignedTo, AssignStrategy: item.AssignStrategy, IsActive: true, Notification: item.Notification, NotificationMetadataV2: item.NotificationMetadata, CircleID: cid, CreatedBy: uid, UpdatedBy: uid, CreatedAt: reservation.AppliedAt, UpdatedAt: reservation.AppliedAt, Points: item.Points, CompletionWindow: item.CompletionWindow, Priority: item.Priority, RequireApproval: item.RequireApproval, IsPrivate: item.IsPrivate, ProjectID: item.ProjectID}
		due, err := chore.InitialDueDate(c, choreModel, start)
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error(), "itemId": item.ID})
			return
		}
		choreModel.NextDueDate = due
		var meter *mModel.ChoreScheduleRequest
		if item.MeterID != nil {
			meter = &mModel.ChoreScheduleRequest{MeterID: *item.MeterID, Interval: *item.MeterInterval}
		}
		assigneeIDs := append([]int(nil), item.AssigneeIDs...)
		if len(assigneeIDs) == 0 {
			if err := h.db.WithContext(c).Model(&cModel.UserCircle{}).Where("circle_id = ? AND is_active = ?", cid, true).Pluck("user_id", &assigneeIDs).Error; err != nil {
				c.JSON(500, gin.H{"error": "Unable to resolve Circle members"})
				return
			}
		}
		specs = append(specs, chRepo.CreateSpec{Chore: choreModel, AssigneeIDs: assigneeIDs, LabelIDs: item.LabelIDs, SubTaskNames: item.SubTaskNames, ThingID: assetID, Meter: meter})
	}
	err = h.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&reservation).Error; err != nil {
			return err
		}
		ids, err := h.chores.CreateChoreSetInTransaction(c, tx, specs, uid)
		if err != nil {
			return err
		}
		reservation.ChoreIDs = ids
		return tx.Save(&reservation).Error
	})
	if err != nil {
		if h.db.WithContext(c).Where("circle_id = ? AND idempotency_key = ?", cid, input.IdempotencyKey).First(&previous).Error == nil {
			c.JSON(200, gin.H{"res": previous, "replayed": true})
			return
		}
		c.JSON(500, gin.H{"error": "Routine application failed; no tasks were created"})
		return
	}
	c.JSON(201, gin.H{"res": reservation})
}

func (h *Handler) validateReferences(c *gin.Context, circleID int, item *Item, selectedAsset *int) error {
	var members int64
	for _, userID := range item.AssigneeIDs {
		if h.db.WithContext(c).Model(&cModel.UserCircle{}).Where("circle_id = ? AND user_id = ? AND is_active = ?", circleID, userID, true).Count(&members); members != 1 {
			return errors.New("an assigned member is no longer active")
		}
	}
	if item.AssignedTo != nil {
		valid := false
		for _, userID := range item.AssigneeIDs {
			if userID == *item.AssignedTo {
				valid = true
			}
		}
		if !valid {
			return errors.New("next assignee must be one of the selected members")
		}
	}
	for _, labelID := range item.LabelIDs {
		var count int64
		h.db.WithContext(c).Model(&lModel.Label{}).Where("id = ? AND circle_id = ?", labelID, circleID).Count(&count)
		if count != 1 {
			return errors.New("label does not belong to this Circle")
		}
	}
	if item.ProjectID != nil {
		var count int64
		h.db.WithContext(c).Table("projects").Where("id = ? AND circle_id = ?", *item.ProjectID, circleID).Count(&count)
		if count != 1 {
			return errors.New("project does not belong to this Circle")
		}
	}
	assetID := item.AssetID
	if selectedAsset != nil && *selectedAsset > 0 {
		assetID = selectedAsset
	}
	if item.RequireAsset && assetID == nil {
		return errors.New("this task requires an Asset selection")
	}
	if assetID != nil {
		var count int64
		h.db.WithContext(c).Model(&tModel.Thing{}).Where("id = ? AND circle_id = ? AND archived_at IS NULL", *assetID, circleID).Count(&count)
		if count != 1 {
			return errors.New("asset does not belong to this Circle")
		}
	}
	if item.MeterID != nil {
		var meter mModel.Meter
		if h.db.WithContext(c).Where("id = ? AND circle_id = ?", *item.MeterID, circleID).First(&meter).Error != nil {
			return errors.New("meter does not belong to this Circle")
		}
		if assetID != nil && (meter.ThingID == nil || *meter.ThingID != *assetID) {
			return errors.New("meter is not linked to the selected Asset")
		}
	}
	return nil
}

func (h *Handler) SaveChore(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok {
		return
	}
	packID, ok := id(c, "id")
	if !ok {
		return
	}
	pack, ok := h.managedPack(c, uid, cid, packID)
	if !ok {
		return
	}
	choreID, ok := id(c, "choreId")
	if !ok {
		return
	}
	source, err := h.chores.GetChore(c, choreID, uid, cid)
	if err != nil {
		c.JSON(404, gin.H{"error": "Task not found"})
		return
	}
	item := Item{PackID: pack.ID, Position: len(pack.Items), Name: source.Name, Description: source.Description, FrequencyType: source.FrequencyType, Frequency: source.Frequency, FrequencyMetadata: source.FrequencyMetadataV2, IsRolling: source.IsRolling, AssignedTo: source.AssignedTo, AssignStrategy: source.AssignStrategy, Points: source.Points, RequireApproval: source.RequireApproval, IsPrivate: source.IsPrivate, Priority: source.Priority, CompletionWindow: source.CompletionWindow, ProjectID: source.ProjectID, Notification: source.Notification, NotificationMetadata: source.NotificationMetadataV2}
	for _, assignee := range source.Assignees {
		item.AssigneeIDs = append(item.AssigneeIDs, assignee.UserID)
	}
	if source.LabelsV2 != nil {
		for _, label := range *source.LabelsV2 {
			item.LabelIDs = append(item.LabelIDs, label.ID)
		}
	}
	if source.SubTasks != nil {
		for _, subtask := range *source.SubTasks {
			item.SubTaskNames = append(item.SubTaskNames, subtask.Name)
		}
	}
	if source.ThingChore != nil {
		item.AssetID = &source.ThingChore.ThingID
	}
	if source.MeterSchedule != nil {
		item.MeterID, item.MeterInterval = &source.MeterSchedule.MeterID, &source.MeterSchedule.Interval
	}
	if err := h.db.WithContext(c).Create(&item).Error; err != nil {
		c.JSON(500, gin.H{"error": "Unable to save task to routine"})
		return
	}
	c.JSON(201, gin.H{"res": item})
}

func Routes(router *gin.Engine, h *Handler, middleware *auth.MultiAuthMiddleware) {
	r := router.Group("api/v1/routines")
	r.Use(middleware.MiddlewareFunc())
	r.GET("", h.List)
	r.POST("", h.Create)
	r.GET("/:id", h.Read)
	r.PUT("/:id", h.Update)
	r.PATCH("/:id/status", h.SetActive)
	r.POST("/:id/items", h.AddItem)
	r.PUT("/:id/items/:itemId", h.UpdateItem)
	r.DELETE("/:id/items/:itemId", h.DeleteItem)
	r.PUT("/:id/items/reorder", h.Reorder)
	r.POST("/:id/apply", h.Apply)
	r.POST("/:id/from-chore/:choreId", h.SaveChore)
}
