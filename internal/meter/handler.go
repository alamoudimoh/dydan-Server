package meter

import (
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"donetick.com/core/internal/auth"
	mModel "donetick.com/core/internal/meter/model"
	mRepo "donetick.com/core/internal/meter/repo"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type Handler struct{ repo *mRepo.Repository }

func NewHandler(repo *mRepo.Repository) *Handler { return &Handler{repo: repo} }

type meterRequest struct {
	Name           string      `json:"name" binding:"required"`
	Unit           mModel.Unit `json:"unit" binding:"required"`
	InitialReading int64       `json:"initialReading"`
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
	_, circleID, ok := current(c)
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
	c.JSON(http.StatusOK, gin.H{"res": meter})
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
	r.GET("/:id/readings", h.History)
}
