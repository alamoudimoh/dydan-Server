package meter

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alamoudimoh/dydan-server/internal/auth"
	choreDomain "github.com/alamoudimoh/dydan-server/internal/chore"
	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	chRepo "github.com/alamoudimoh/dydan-server/internal/chore/repo"
	mModel "github.com/alamoudimoh/dydan-server/internal/meter/model"
	mRepo "github.com/alamoudimoh/dydan-server/internal/meter/repo"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Handler struct {
	repo      *mRepo.Repository
	choreRepo *chRepo.ChoreRepository
}

func NewHandler(repo *mRepo.Repository, choreRepo *chRepo.ChoreRepository) *Handler {
	return &Handler{repo: repo, choreRepo: choreRepo}
}

type reminderRequest struct {
	Enabled           bool                       `json:"enabled"`
	FrequencyType     chModel.FrequencyType      `json:"frequencyType"`
	Frequency         int                        `json:"frequency"`
	FrequencyMetadata *chModel.FrequencyMetadata `json:"frequencyMetadata"`
	NextDueDate       *time.Time                 `json:"nextDueDate"`
	AssignedTo        *int                       `json:"assignedTo"`
}

type meterRequest struct {
	Name           string           `json:"name" binding:"required"`
	Unit           mModel.Unit      `json:"unit" binding:"required"`
	InitialReading int64            `json:"initialReading"`
	Reminder       *reminderRequest `json:"reminder"`
}

type completeReminderRequest struct {
	Reading     int64      `json:"reading"`
	ExpectedDue *time.Time `json:"expectedDue"`
}

func (h *Handler) configureReminder(c *gin.Context, meter *mModel.Meter, req *reminderRequest, actorID, circleID int) error {
	if req == nil {
		return nil
	}
	assigned := actorID
	if req.AssignedTo != nil {
		assigned = *req.AssignedTo
	}
	spec := chRepo.MeterReminderSpec{Enabled: req.Enabled, FrequencyType: req.FrequencyType, Frequency: req.Frequency, FrequencyMetadata: req.FrequencyMetadata, NextDueDate: req.NextDueDate, AssignedTo: assigned}
	if req.Enabled {
		if req.Frequency < 1 || req.FrequencyType == "" || req.FrequencyType == chModel.FrequencyTypeOnce {
			return errors.New("a recurring update frequency is required")
		}
		if spec.NextDueDate == nil {
			chore := &chModel.Chore{FrequencyType: req.FrequencyType, Frequency: req.Frequency, FrequencyMetadataV2: req.FrequencyMetadata}
			due, err := choreDomain.InitialDueDate(c, chore, time.Now().UTC())
			if err != nil {
				return err
			}
			spec.NextDueDate = due
		}
	}
	return h.choreRepo.ConfigureMeterReminder(c, meter.ID, circleID, actorID, spec)
}

type readingRequest struct {
	Reading    int64   `json:"reading"`
	Correction bool    `json:"correction"`
	Note       *string `json:"note"`
}

func current(c *gin.Context) (int, int, bool) {
	u, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication failed"})
		return 0, 0, false
	}
	return u.ID, u.CircleID, true
}

func meterID(c *gin.Context) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid meter ID"})
		return 0, false
	}
	return id, true
}

func (h *Handler) Create(c *gin.Context) {
	actorID, circleID, ok := current(c)
	if !ok {
		return
	}
	var req meterRequest
	if err := c.ShouldBindJSON(&req); err != nil || !req.Unit.Valid() || req.InitialReading < 0 || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid meter"})
		return
	}
	now := time.Now().UTC()
	meter := &mModel.Meter{CircleID: circleID, CreatedBy: actorID, Name: strings.TrimSpace(req.Name), Unit: req.Unit, CurrentReading: req.InitialReading, CreatedAt: now, UpdatedAt: now}
	if err := h.repo.Create(c, meter, actorID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to create meter"})
		return
	}
	if err := h.configureReminder(c, meter, req.Reminder, actorID, circleID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusCreated, gin.H{"res": meter})
}

func (h *Handler) List(c *gin.Context) {
	_, circleID, ok := current(c)
	if !ok {
		return
	}
	meters, err := h.repo.List(c, circleID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Failed to retrieve meters"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": meters})
}

func (h *Handler) Update(c *gin.Context) {
	actorID, circleID, ok := current(c)
	if !ok {
		return
	}
	id, ok := meterID(c)
	if !ok {
		return
	}
	var req meterRequest
	if err := c.ShouldBindJSON(&req); err != nil || !req.Unit.Valid() || strings.TrimSpace(req.Name) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid meter"})
		return
	}
	if err := h.repo.UpdateMetadata(c, id, circleID, strings.TrimSpace(req.Name), req.Unit); err != nil {
		writeRepoError(c, err)
		return
	}
	meter, _ := h.repo.Get(c, id, circleID)
	if err := h.configureReminder(c, meter, req.Reminder, actorID, circleID); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": meter})
}

func (h *Handler) CompleteReminder(c *gin.Context) {
	actorID, circleID, ok := current(c)
	if !ok {
		return
	}
	id, ok := meterID(c)
	if !ok {
		return
	}
	var req completeReminderRequest
	if c.ShouldBindJSON(&req) != nil || req.Reading < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid reading"})
		return
	}
	meter, err := h.repo.Get(c, id, circleID)
	if err != nil {
		writeRepoError(c, err)
		return
	}
	var reminder mModel.UpdateReminder
	if meter.UpdateReminder != nil {
		reminder = *meter.UpdateReminder
	} else {
		c.JSON(http.StatusNotFound, gin.H{"error": "Meter update reminder not found"})
		return
	}
	chore := &chModel.Chore{ID: reminder.ChoreID, FrequencyType: ""}
	// Fetching through the circle-scoped repository also provides recurrence metadata.
	full, err := h.choreRepo.GetChore(c, reminder.ChoreID, actorID, circleID)
	if err != nil {
		c.JSON(http.StatusNotFound, gin.H{"error": "Meter update task not found"})
		return
	}
	chore = full
	next, err := choreDomain.ScheduleNextDueDate(c, chore, time.Now().UTC())
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	err = h.choreRepo.CompleteMeterReminder(c, id, circleID, actorID, req.Reading, req.ExpectedDue, next)
	if errors.Is(err, mRepo.ErrDecreasingReading) {
		c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf("Reading cannot be lower than the current reading of %d %s.", meter.CurrentReading, meter.Unit)})
		return
	}
	if errors.Is(err, chRepo.ErrMeterReminderAlreadyCompleted) {
		updated, _ := h.repo.Get(c, id, circleID)
		c.JSON(http.StatusOK, gin.H{"res": updated, "idempotent": true})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	updated, _ := h.repo.Get(c, id, circleID)
	c.JSON(http.StatusOK, gin.H{"res": updated})
}

func (h *Handler) AddReading(c *gin.Context) {
	actorID, circleID, ok := current(c)
	if !ok {
		return
	}
	id, ok := meterID(c)
	if !ok {
		return
	}
	var req readingRequest
	if err := c.ShouldBindJSON(&req); err != nil || req.Reading < 0 || (req.Correction && (req.Note == nil || strings.TrimSpace(*req.Note) == "")) {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid reading; corrections require a note"})
		return
	}
	meter, err := h.repo.AddReading(c, id, circleID, req.Reading, actorID, req.Correction, req.Note)
	if errors.Is(err, mRepo.ErrDecreasingReading) {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if err != nil {
		writeRepoError(c, err)
		return
	}
	if err := h.repo.MarkLinkedChoresDue(c, id, circleID); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Reading saved but linked chores could not be updated"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": meter})
}

func (h *Handler) History(c *gin.Context) {
	_, circleID, ok := current(c)
	if !ok {
		return
	}
	id, ok := meterID(c)
	if !ok {
		return
	}
	offset, err := strconv.Atoi(c.DefaultQuery("offset", "0"))
	if err != nil || offset < 0 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid offset"})
		return
	}
	readings, err := h.repo.History(c, id, circleID, offset)
	if err != nil {
		writeRepoError(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": readings})
}

func writeRepoError(c *gin.Context, err error) {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": "Meter not found"})
		return
	}
	c.JSON(http.StatusInternalServerError, gin.H{"error": "Meter operation failed"})
}

func Routes(router *gin.Engine, h *Handler, middleware *auth.MultiAuthMiddleware) {
	r := router.Group("api/v1/meters")
	r.Use(middleware.MiddlewareFunc())
	r.GET("", h.List)
	r.POST("", h.Create)
	r.PUT("/:id", h.Update)
	r.POST("/:id/readings", h.AddReading)
	r.POST("/:id/reminder/complete", h.CompleteReminder)
	r.GET("/:id/readings", h.History)
}
