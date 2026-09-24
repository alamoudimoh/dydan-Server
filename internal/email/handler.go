package email

import (
	"net/http"
	"net/mail"
	"strings"

	"github.com/alamoudimoh/dydan-server/internal/auth"
	cRepo "github.com/alamoudimoh/dydan-server/internal/circle/repo"
	"github.com/gin-gonic/gin"
)

// Handler exposes only masked delivery settings. Its routes are intentionally
// separate from per-account preferences: changing an instance email provider
// is an administrator operation.
type Handler struct {
	store   *RuntimeStore
	sender  Sender
	circles *cRepo.CircleRepository
}

func NewHandler(store *RuntimeStore, sender Sender, circles *cRepo.CircleRepository) *Handler {
	return &Handler{store: store, sender: sender, circles: circles}
}

func (h *Handler) requireAdministrator(c *gin.Context) bool {
	user, ok := auth.CurrentUser(c)
	if !ok {
		c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "Authentication required"})
		return false
	}
	role, err := h.circles.GetActiveUserCircleRole(c, user.CircleID, user.ID)
	if err != nil || auth.ResolveEffectiveRole(&user.User, role, err == nil) != auth.EffectiveRoleOwner {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"error": "DyDan administrator permission required"})
		return false
	}
	return true
}

func (h *Handler) get(c *gin.Context) {
	if !h.requireAdministrator(c) {
		return
	}
	status, err := h.store.Status(c)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to load email delivery configuration"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": status})
}

func (h *Handler) save(c *gin.Context) {
	if !h.requireAdministrator(c) {
		return
	}
	var input DeliveryInput
	if err := c.ShouldBindJSON(&input); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid email delivery configuration"})
		return
	}
	status, err := h.store.Save(c, input)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": status})
}

func (h *Handler) test(c *gin.Context) {
	if !h.requireAdministrator(c) {
		return
	}
	var request struct {
		To string `json:"to"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || strings.TrimSpace(request.To) == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "A test recipient email is required"})
		return
	}
	if _, err := mail.ParseAddress(request.To); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "A valid test recipient email is required"})
		return
	}
	if err := h.sender.SendTestEmail(c, request.To); err != nil {
		// Do not reflect provider/SMTP diagnostic strings: some SMTP servers echo
		// authentication exchanges. The result still accurately communicates that
		// delivery failed without creating a credential disclosure path.
		c.JSON(http.StatusBadGateway, gin.H{"error": "The configured provider could not deliver the test email"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": gin.H{"delivered": true, "message": "Test email accepted by the configured provider"}})
}

func Routes(router *gin.Engine, h *Handler, middleware *auth.MultiAuthMiddleware) {
	g := router.Group("/api/v1/admin/email-delivery")
	g.Use(middleware.MiddlewareFunc())
	g.GET("", h.get)
	g.PUT("", h.save)
	g.POST("/test", h.test)
}
