package label

import (
	"errors"
	"net/http"
	"strconv"

	auth "github.com/alamoudimoh/daydan-server/internal/auth"
	lModel "github.com/alamoudimoh/daydan-server/internal/label/model"
	lRepo "github.com/alamoudimoh/daydan-server/internal/label/repo"
	jwt "github.com/appleboy/gin-jwt/v2"
	"github.com/gin-gonic/gin"
)

type LabelReq struct {
	Name  string `json:"name" binding:"required"`
	Color string `json:"color"`
}

type UpdateLabelReq struct {
	ID int `json:"id" binding:"required"`
	LabelReq
}

type Handler struct {
	lRepo *lRepo.LabelRepository
}

func NewHandler(lRepo *lRepo.LabelRepository) *Handler {
	return &Handler{
		lRepo: lRepo,
	}
}

func (h *Handler) getLabels(c *gin.Context) {
	// get current user:
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(500, gin.H{
			"error": "Error getting current user",
		})
		return
	}

	labels, err := h.lRepo.GetUserLabels(c, currentUser.ID, currentUser.CircleID)
	if err != nil {
		c.JSON(500, gin.H{
			"error": "Error getting labels",
		})
		return
	}
	c.JSON(200,
		labels,
	)
}

func (h *Handler) createLabel(c *gin.Context) {
	// get current user:
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(500, gin.H{
			"error": "Error getting current user",
		})
		return
	}

	var req LabelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{
			"error": "Invalid request: " + err.Error(),
		})
		return
	}

	label := &lModel.Label{
		Name:      req.Name,
		Color:     req.Color,
		CreatedBy: currentUser.ID,
		CircleID:  &currentUser.CircleID,
	}
	if err := h.lRepo.CreateLabels(c, []*lModel.Label{label}); err != nil {
		c.JSON(500, gin.H{
			"error": "Error creating label",
		})
		return
	}

	c.JSON(200, gin.H{
		"res": label,
	})
}

func (h *Handler) getCategories(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Unauthorized"})
		return
	}
	labels, err := h.lRepo.GetCircleCategories(c, currentUser.CircleID, c.Query("includeArchived") == "true")
	if err != nil {
		c.JSON(500, gin.H{"error": "Error getting categories"})
		return
	}
	c.JSON(200, gin.H{"res": labels})
}

func categoryError(c *gin.Context, err error) {
	if errors.Is(err, lRepo.ErrDuplicateCategory) {
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	if errors.Is(err, lRepo.ErrCategoryNotFound) {
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
}

func (h *Handler) createCategory(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(401, gin.H{"error": "Unauthorized"})
		return
	}
	var req LabelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Invalid request"})
		return
	}
	label := &lModel.Label{Name: req.Name, Color: req.Color, CreatedBy: currentUser.ID, CircleID: &currentUser.CircleID}
	if err := h.lRepo.CreateCategory(c, label); err != nil {
		categoryError(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"res": label})
}

func (h *Handler) updateCategory(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(401, gin.H{"error": "Unauthorized"})
		return
	}
	var req UpdateLabelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Invalid request"})
		return
	}
	label := &lModel.Label{ID: req.ID, Name: req.Name}
	if err := h.lRepo.UpdateCategory(c, currentUser.CircleID, label); err != nil {
		categoryError(c, err)
		return
	}
	c.JSON(200, gin.H{"res": label})
}

func (h *Handler) archiveCategory(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(401, gin.H{"error": "Unauthorized"})
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil {
		c.JSON(400, gin.H{"error": "Invalid category ID"})
		return
	}
	var req struct {
		Archived bool `json:"archived"`
	}
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{"error": "Invalid request"})
		return
	}
	if err := h.lRepo.SetCategoryArchived(c, currentUser.CircleID, id, req.Archived); err != nil {
		categoryError(c, err)
		return
	}
	c.Status(http.StatusNoContent)
}

func (h *Handler) updateLabel(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(500, gin.H{
			"error": "Error getting current user",
		})
		return
	}

	var req UpdateLabelReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, gin.H{
			"error": "Invalid request: " + err.Error(),
		})
		return
	}

	label := &lModel.Label{
		Name:     req.Name,
		Color:    req.Color,
		ID:       req.ID,
		CircleID: &currentUser.CircleID,
	}
	if err := h.lRepo.UpdateLabel(c, currentUser.ID, label); err != nil {
		c.JSON(500, gin.H{
			"error": "Error updating label",
		})
		return
	}

	c.JSON(200, gin.H{
		"res": label,
	})
}

func (h *Handler) deleteLabel(c *gin.Context) {
	currentUser, ok := auth.CurrentUser(c)
	// read label id from path:

	if !ok {
		c.JSON(500, gin.H{
			"error": "Error getting current user",
		})
		return
	}

	labelIDRaw := c.Param("id")
	if labelIDRaw == "" {
		c.JSON(400, gin.H{
			"error": "Label ID is required",
		})
		return
	}

	labelID, err := strconv.Atoi(labelIDRaw)
	if err != nil {
		c.JSON(400, gin.H{
			"error": "Invalid label ID",
		})
		return
	}

	// unassociate label from all chores:
	if err := h.lRepo.DeassignLabelFromAllChoreAndDelete(c, currentUser.ID, labelID); err != nil {
		c.JSON(500, gin.H{
			"error": "Error unassociating label from chores",
		})
		return
	}

	c.JSON(200, gin.H{
		"res": "Label deleted",
	})

}

func Routes(r *gin.Engine, h *Handler, auth *jwt.GinJWTMiddleware) {

	labelRoutes := r.Group("api/v1/labels")
	labelRoutes.Use(auth.MiddlewareFunc())
	{
		labelRoutes.GET("", h.getLabels)
		labelRoutes.POST("", h.createLabel)
		labelRoutes.PUT("", h.updateLabel)
		labelRoutes.DELETE("/:id", h.deleteLabel)
	}
	categoryRoutes := r.Group("api/v1/categories")
	categoryRoutes.Use(auth.MiddlewareFunc())
	{
		categoryRoutes.GET("", h.getCategories)
		categoryRoutes.POST("", h.createCategory)
		categoryRoutes.PUT("", h.updateCategory)
		categoryRoutes.PATCH("/:id/archive", h.archiveCategory)
	}

}
