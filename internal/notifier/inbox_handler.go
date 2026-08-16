package notifier

import (
	"github.com/alamoudimoh/daydan-server/internal/auth"
	nRepo "github.com/alamoudimoh/daydan-server/internal/notifier/repo"
	"github.com/gin-gonic/gin"
	"net/http"
	"strconv"
)

type InboxHandler struct{ repo *nRepo.InboxRepository }

func NewInboxHandler(repo *nRepo.InboxRepository) *InboxHandler { return &InboxHandler{repo: repo} }
func inboxUser(c *gin.Context) (int, int, bool) {
	u, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return 0, 0, false
	}
	return u.ID, u.CircleID, true
}
func (h *InboxHandler) List(c *gin.Context) {
	uid, cid, ok := inboxUser(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "30"))
	before, _ := strconv.Atoi(c.Query("before"))
	items, err := h.repo.List(c, uid, cid, limit, before)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to retrieve notifications"})
		return
	}
	count, err := h.repo.UnreadCount(c, uid, cid)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to retrieve notifications"})
		return
	}
	c.JSON(200, gin.H{"res": items, "unreadCount": count})
}
func (h *InboxHandler) Count(c *gin.Context) {
	uid, cid, ok := inboxUser(c)
	if !ok {
		return
	}
	count, err := h.repo.UnreadCount(c, uid, cid)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to retrieve unread count"})
		return
	}
	c.JSON(200, gin.H{"unreadCount": count})
}
func (h *InboxHandler) Read(c *gin.Context) {
	uid, cid, ok := inboxUser(c)
	if !ok {
		return
	}
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(400, gin.H{"error": "Invalid notification ID"})
		return
	}
	changed, err := h.repo.MarkRead(c, id, uid, cid)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to mark notification read"})
		return
	}
	if !changed {
		c.JSON(404, gin.H{"error": "Notification not found"})
		return
	}
	c.Status(204)
}
func (h *InboxHandler) ReadAll(c *gin.Context) {
	uid, cid, ok := inboxUser(c)
	if !ok {
		return
	}
	_, err := h.repo.MarkAllRead(c, uid, cid)
	if err != nil {
		c.JSON(500, gin.H{"error": "Failed to mark notifications read"})
		return
	}
	c.Status(204)
}
func InboxRoutes(router *gin.Engine, h *InboxHandler, m *auth.MultiAuthMiddleware) {
	r := router.Group("api/v1/notifications")
	r.Use(m.MiddlewareFunc())
	r.GET("", h.List)
	r.GET("/unread-count", h.Count)
	r.PATCH("/:id/read", h.Read)
	r.POST("/read-all", h.ReadAll)
}
