package shoppinglist

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/alamoudimoh/daydan-server/internal/auth"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/daydan-server/internal/circle/repo"
	slModel "github.com/alamoudimoh/daydan-server/internal/shoppinglist/model"
	slRepo "github.com/alamoudimoh/daydan-server/internal/shoppinglist/repo"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	repo    *slRepo.Repository
	circles *cRepo.CircleRepository
}

func NewHandler(repo *slRepo.Repository, circles *cRepo.CircleRepository) *Handler {
	return &Handler{repo: repo, circles: circles}
}

func (h *Handler) current(c *gin.Context) (*uModel.UserDetails, bool) {
	u, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return nil, false
	}
	return u, true
}

func (h *Handler) membership(c *gin.Context, circleID, userID int) (cModel.Role, bool) {
	if circleID < 1 {
		return "", false
	}
	role, err := h.circles.GetActiveUserCircleRole(c, circleID, userID)
	if err != nil {
		return role, false
	}
	// Managed (child) accounts never exercise elevated capabilities even if a
	// stale or inconsistent membership row grants them an elevated role.
	if u, ok := auth.CurrentUser(c); ok {
		role = auth.CapEffectiveCircleRole(&u.User, role)
	}
	return role, true
}

func (h *Handler) isCircleAdmin(role cModel.Role) bool {
	return role == cModel.RoleAdmin
}

func parseID(c *gin.Context, param string) (int, bool) {
	id, err := strconv.Atoi(c.Param(param))
	if err != nil || id < 1 {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid ID"})
		return 0, false
	}
	return id, true
}

func writeRepoErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, slRepo.ErrListNotFound), errors.Is(err, slRepo.ErrItemNotFound):
		c.JSON(http.StatusNotFound, gin.H{"error": err.Error()})
	case errors.Is(err, slRepo.ErrForbidden):
		c.JSON(http.StatusForbidden, gin.H{"error": err.Error()})
	case errors.Is(err, slRepo.ErrInvalidScope), errors.Is(err, slRepo.ErrInvalidName), errors.Is(err, slRepo.ErrInvalidItem):
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
	case errors.Is(err, slRepo.ErrArchived):
		c.JSON(http.StatusConflict, gin.H{"error": err.Error()})
	default:
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to complete shopping list operation"})
	}
}

func (h *Handler) listLists(c *gin.Context) {
	user, ok := h.current(c)
	if !ok {
		return
	}
	_, circleMember := h.membership(c, user.CircleID, user.ID)
	includeArchived := c.Query("includeArchived") == "true"
	if includeArchived {
		role, active := h.membership(c, user.CircleID, user.ID)
		if !active || !h.isCircleAdmin(role) {
			c.JSON(http.StatusForbidden, gin.H{"error": "Circle admin permission required"})
			return
		}
	}

	lists, err := h.repo.ListAccessible(c, user.ID, user.CircleID, circleMember, includeArchived)
	if err != nil {
		writeRepoErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": lists})
}

func (h *Handler) getList(c *gin.Context) {
	user, ok := h.current(c)
	if !ok {
		return
	}
	listID, ok := parseID(c, "id")
	if !ok {
		return
	}
	_, circleMember := h.membership(c, user.CircleID, user.ID)

	list, err := h.repo.GetByID(c, listID)
	if err != nil {
		writeRepoErr(c, err)
		return
	}
	if !slRepo.CanAccessList(user.ID, user.CircleID, list, circleMember) {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	detail, err := h.repo.GetDetail(c, listID)
	if err != nil {
		writeRepoErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": detail})
}

func (h *Handler) createList(c *gin.Context) {
	user, ok := h.current(c)
	if !ok {
		return
	}
	var req slModel.CreateListReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	_, circleMember := h.membership(c, user.CircleID, user.ID)

	list, err := h.repo.CreateList(c, user.ID, user.CircleID, circleMember, req)
	if err != nil {
		writeRepoErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"res": list})
}

func (h *Handler) updateList(c *gin.Context) {
	user, ok := h.current(c)
	if !ok {
		return
	}
	listID, ok := parseID(c, "id")
	if !ok {
		return
	}
	var req slModel.UpdateListReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	role, circleMember := h.membership(c, user.CircleID, user.ID)

	list, err := h.repo.GetByID(c, listID)
	if err != nil {
		writeRepoErr(c, err)
		return
	}
	if !slRepo.CanManageList(user.ID, user.CircleID, list, circleMember, h.isCircleAdmin(role)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}

	if err := h.repo.RenameList(c, list, req.Name); err != nil {
		writeRepoErr(c, err)
		return
	}
	updated, err := h.repo.GetByID(c, listID)
	if err != nil {
		writeRepoErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": updated})
}

func (h *Handler) archiveList(c *gin.Context) {
	user, ok := h.current(c)
	if !ok {
		return
	}
	listID, ok := parseID(c, "id")
	if !ok {
		return
	}
	role, circleMember := h.membership(c, user.CircleID, user.ID)

	list, err := h.repo.GetByID(c, listID)
	if err != nil {
		writeRepoErr(c, err)
		return
	}
	if !slRepo.CanManageList(user.ID, user.CircleID, list, circleMember, h.isCircleAdmin(role)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}
	if err := h.repo.ArchiveList(c, listID); err != nil {
		writeRepoErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": "archived"})
}

func (h *Handler) deleteList(c *gin.Context) {
	user, ok := h.current(c)
	if !ok {
		return
	}
	listID, ok := parseID(c, "id")
	if !ok {
		return
	}
	role, circleMember := h.membership(c, user.CircleID, user.ID)

	list, err := h.repo.GetByID(c, listID)
	if err != nil {
		writeRepoErr(c, err)
		return
	}
	if !slRepo.CanManageList(user.ID, user.CircleID, list, circleMember, h.isCircleAdmin(role)) {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return
	}
	if err := h.repo.DeleteList(c, listID); err != nil {
		writeRepoErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": "deleted"})
}

func (h *Handler) loadListForEdit(c *gin.Context, user *uModel.UserDetails) (*slModel.ShoppingList, bool) {
	listID, ok := parseID(c, "id")
	if !ok {
		return nil, false
	}
	_, circleMember := h.membership(c, user.CircleID, user.ID)
	list, err := h.repo.GetByID(c, listID)
	if err != nil {
		writeRepoErr(c, err)
		return nil, false
	}
	if list.ArchivedAt != nil {
		writeRepoErr(c, slRepo.ErrArchived)
		return nil, false
	}
	if !slRepo.CanEditItems(user.ID, user.CircleID, list, circleMember) {
		c.JSON(http.StatusForbidden, gin.H{"error": "permission denied"})
		return nil, false
	}
	return list, true
}

func (h *Handler) createItem(c *gin.Context) {
	user, ok := h.current(c)
	if !ok {
		return
	}
	list, ok := h.loadListForEdit(c, user)
	if !ok {
		return
	}
	var req slModel.CreateItemReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	item, err := h.repo.CreateItem(c, list.ID, user.ID, req)
	if err != nil {
		writeRepoErr(c, err)
		return
	}
	c.JSON(http.StatusCreated, gin.H{"res": item})
}

func (h *Handler) updateItem(c *gin.Context) {
	user, ok := h.current(c)
	if !ok {
		return
	}
	list, ok := h.loadListForEdit(c, user)
	if !ok {
		return
	}
	itemID, ok := parseID(c, "itemId")
	if !ok {
		return
	}
	var req slModel.UpdateItemReq
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid request"})
		return
	}
	item, err := h.repo.UpdateItem(c, itemID, list.ID, user.ID, req)
	if err != nil {
		writeRepoErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": item})
}

func (h *Handler) deleteItem(c *gin.Context) {
	user, ok := h.current(c)
	if !ok {
		return
	}
	list, ok := h.loadListForEdit(c, user)
	if !ok {
		return
	}
	itemID, ok := parseID(c, "itemId")
	if !ok {
		return
	}
	if err := h.repo.DeleteItem(c, itemID, list.ID); err != nil {
		writeRepoErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": "deleted"})
}

func (h *Handler) clearCompleted(c *gin.Context) {
	user, ok := h.current(c)
	if !ok {
		return
	}
	list, ok := h.loadListForEdit(c, user)
	if !ok {
		return
	}
	removed, err := h.repo.ClearCompleted(c, list.ID)
	if err != nil {
		writeRepoErr(c, err)
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": gin.H{"removed": removed}})
}

func Routes(r *gin.Engine, h *Handler, multiAuthMiddleware *auth.MultiAuthMiddleware) {
	g := r.Group("api/v1/shopping-lists")
	g.Use(multiAuthMiddleware.MiddlewareFunc())
	{
		g.GET("", h.listLists)
		g.POST("", h.createList)
		g.GET("/:id", h.getList)
		g.PUT("/:id", h.updateList)
		g.POST("/:id/archive", h.archiveList)
		g.DELETE("/:id", h.deleteList)
		g.POST("/:id/items", h.createItem)
		g.PUT("/:id/items/:itemId", h.updateItem)
		g.DELETE("/:id/items/:itemId", h.deleteItem)
		g.POST("/:id/clear-completed", h.clearCompleted)
	}
}
