package reward

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/alamoudimoh/daydan-server/internal/auth"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/daydan-server/internal/circle/repo"
	nModel "github.com/alamoudimoh/daydan-server/internal/notifier/model"
	nService "github.com/alamoudimoh/daydan-server/internal/notifier/service"
	"github.com/gin-gonic/gin"
)

type Handler struct {
	repo    *Repository
	circles *cRepo.CircleRepository
	inbox   *nService.InboxService
}

func NewHandler(repo *Repository, circles *cRepo.CircleRepository, inbox *nService.InboxService) *Handler {
	return &Handler{repo: repo, circles: circles, inbox: inbox}
}
func current(c *gin.Context) (int, int, bool) {
	u, ok := auth.CurrentUser(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return 0, 0, false
	}
	return u.ID, u.CircleID, true
}
func (h *Handler) admin(c *gin.Context, circleID, userID int) bool {
	role, active := h.membership(c, circleID, userID)
	if !active || role != cModel.RoleAdmin {
		c.JSON(http.StatusForbidden, gin.H{"error": "Circle admin permission required"})
		return false
	}
	return true
}
func (h *Handler) membership(c *gin.Context, circleID, userID int) (cModel.Role, bool) {
	role, err := h.circles.GetActiveUserCircleRole(c, circleID, userID)
	return role, err == nil
}
func parseID(c *gin.Context) (int, bool) {
	id, err := strconv.Atoi(c.Param("id"))
	if err != nil || id < 1 {
		c.JSON(400, gin.H{"error": "Invalid ID"})
		return 0, false
	}
	return id, true
}
func writeErr(c *gin.Context, err error) {
	switch {
	case errors.Is(err, ErrNotFound):
		c.JSON(404, gin.H{"error": "Reward not found"})
	case errors.Is(err, ErrInactive):
		c.JSON(409, gin.H{"error": "This reward is archived"})
	case errors.Is(err, ErrInsufficient):
		c.JSON(409, gin.H{"error": "Insufficient points"})
	case errors.Is(err, ErrResolved):
		c.JSON(409, gin.H{"error": "Request is already resolved"})
	default:
		c.JSON(500, gin.H{"error": "Unable to complete reward operation"})
	}
}
func (h *Handler) list(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok {
		return
	}
	if _, active := h.membership(c, cid, uid); !active {
		c.JSON(http.StatusForbidden, gin.H{"error": "Active Circle membership required"})
		return
	}
	include := c.Query("includeArchived") == "true"
	if include && !h.admin(c, cid, uid) {
		return
	}
	v, e := h.repo.ListRewards(c, cid, include)
	if e != nil {
		writeErr(c, e)
		return
	}
	c.JSON(200, gin.H{"res": v})
}

type rewardInput struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	CostPoints  int    `json:"costPoints"`
	IsActive    *bool  `json:"isActive"`
}

func input(c *gin.Context) (rewardInput, bool) {
	var v rewardInput
	if c.ShouldBindJSON(&v) != nil {
		c.JSON(400, gin.H{"error": "Invalid reward"})
		return v, false
	}
	v.Name = strings.TrimSpace(v.Name)
	v.Description = strings.TrimSpace(v.Description)
	if v.Name == "" || len(v.Name) > 120 || v.CostPoints < 1 {
		c.JSON(400, gin.H{"error": "Name is required and cost must be a positive integer"})
		return v, false
	}
	return v, true
}
func (h *Handler) create(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok || !h.admin(c, cid, uid) {
		return
	}
	v, ok := input(c)
	if !ok {
		return
	}
	r := Reward{CircleID: cid, Name: v.Name, Description: v.Description, CostPoints: v.CostPoints, IsActive: true, CreatedBy: uid}
	if e := h.repo.Create(c, &r); e != nil {
		writeErr(c, e)
		return
	}
	c.JSON(201, gin.H{"res": r})
}
func (h *Handler) update(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok || !h.admin(c, cid, uid) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	v, ok := input(c)
	if !ok {
		return
	}
	r, e := h.repo.Update(c, cid, id, v.Name, v.Description, v.CostPoints, v.IsActive)
	if e != nil {
		writeErr(c, e)
		return
	}
	c.JSON(200, gin.H{"res": r})
}
func (h *Handler) request(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok {
		return
	}
	var v struct {
		RewardID int `json:"rewardId"`
	}
	if c.ShouldBindJSON(&v) != nil || v.RewardID < 1 {
		c.JSON(400, gin.H{"error": "Invalid reward"})
		return
	}
	r, e := h.repo.Request(c, cid, uid, v.RewardID)
	if e != nil {
		writeErr(c, e)
		return
	}
	if h.inbox != nil {
		if members, err := h.circles.GetCircleUsers(c, cid); err == nil {
			for _, member := range members {
				if member.IsActive && member.Role == cModel.UserRoleAdmin {
					id := r.ID
					_ = h.inbox.Notify(c, &nModel.InboxNotification{CircleID: cid, UserID: member.UserID, Type: "reward_request_pending", Severity: "warning", Title: "New reward request", Message: r.RewardNameSnapshot + " · " + strconv.Itoa(r.CostPointsSnapshot) + " pts", EntityType: "reward_request", EntityID: &id, DedupKey: "reward_request_pending:" + strconv.Itoa(r.ID) + ":" + strconv.Itoa(member.UserID)})
				}
			}
		}
	}
	c.JSON(201, gin.H{"res": r})
}
func (h *Handler) my(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok {
		return
	}
	r, e := h.repo.ListRequests(c, cid, &uid, nil)
	if e != nil {
		writeErr(c, e)
		return
	}
	b, e := h.repo.Balance(c, cid, uid)
	if e != nil {
		writeErr(c, e)
		return
	}
	c.JSON(200, gin.H{"res": r, "balance": b})
}
func (h *Handler) adminRequests(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok || !h.admin(c, cid, uid) {
		return
	}
	var status *RequestStatus
	if s := c.Query("status"); s != "" {
		v := RequestStatus(s)
		if v != RequestStatusPending && v != RequestStatusApproved && v != RequestStatusRejected && v != RequestStatusCancelled {
			c.JSON(400, gin.H{"error": "Invalid status"})
			return
		}
		status = &v
	}
	r, e := h.repo.ListRequests(c, cid, nil, status)
	if e != nil {
		writeErr(c, e)
		return
	}
	c.JSON(200, gin.H{"res": r})
}
func (h *Handler) resolve(c *gin.Context, status RequestStatus) {
	uid, cid, ok := current(c)
	if !ok || !h.admin(c, cid, uid) {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	r, e := h.repo.Resolve(c, cid, id, uid, status)
	if e != nil {
		writeErr(c, e)
		return
	}
	typeName, severity, title := "reward_request_rejected", "danger", r.RewardNameSnapshot+" request rejected"
	if status == RequestStatusApproved {
		typeName, severity, title = "reward_request_approved", "success", r.RewardNameSnapshot+" approved"
	}
	entityID := r.ID
	if h.inbox != nil {
		_ = h.inbox.Notify(c, &nModel.InboxNotification{CircleID: cid, UserID: r.UserID, Type: typeName, Severity: severity, Title: title, Message: strconv.Itoa(r.CostPointsSnapshot) + " pts", EntityType: "reward_request", EntityID: &entityID, DedupKey: typeName + ":" + strconv.Itoa(r.ID) + ":" + strconv.Itoa(r.UserID)})
	}
	c.JSON(200, gin.H{"res": r})
}
func (h *Handler) cancel(c *gin.Context) {
	uid, cid, ok := current(c)
	if !ok {
		return
	}
	id, ok := parseID(c)
	if !ok {
		return
	}
	if e := h.repo.Cancel(c, cid, id, uid); e != nil {
		writeErr(c, e)
		return
	}
	c.Status(204)
}
func Routes(router *gin.Engine, h *Handler, m *auth.MultiAuthMiddleware) {
	g := router.Group("api/v1/rewards")
	g.Use(m.MiddlewareFunc())
	g.GET("", h.list)
	g.POST("", h.create)
	g.PUT("/:id", h.update)
	g.POST("/requests", h.request)
	g.GET("/requests/mine", h.my)
	g.GET("/requests", h.adminRequests)
	g.POST("/requests/:id/approve", func(c *gin.Context) { h.resolve(c, RequestStatusApproved) })
	g.POST("/requests/:id/reject", func(c *gin.Context) { h.resolve(c, RequestStatusRejected) })
	g.POST("/requests/:id/cancel", h.cancel)
}
