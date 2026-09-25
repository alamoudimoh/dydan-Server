package user

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	auth "github.com/alamoudimoh/dydan-server/internal/auth"
	"github.com/alamoudimoh/dydan-server/internal/preferences"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/alamoudimoh/dydan-server/logging"
	"github.com/gin-gonic/gin"
)

// Preferences is the persisted, non-sensitive subset of the approved Settings
// screen. Integrations are deliberately absent: their external authorization
// flows need dedicated credentials instead of a misleading boolean switch.
type Preferences struct {
	Theme            string                  `json:"theme"`
	Appearance       string                  `json:"appearance"`
	DefaultView      string                  `json:"defaultView"`
	CompactMode      bool                    `json:"compactMode"`
	Language         string                  `json:"language"`
	DateFormat       string                  `json:"dateFormat"`
	TimeFormat       string                  `json:"timeFormat"`
	FirstDayOfWeek   int                     `json:"firstDayOfWeek"`
	Currency         string                  `json:"currency"`
	SidebarCollapsed bool                    `json:"sidebarCollapsed"`
	ShowItemBadges   bool                    `json:"showItemBadges"`
	RememberSidebar  bool                    `json:"rememberSidebar"`
	Notifications    NotificationPreferences `json:"notifications"`
	Privacy          PrivacyPreferences      `json:"privacy"`
	BetaFeatures     bool                    `json:"betaFeatures"`
	VerboseLogging   bool                    `json:"verboseLogging"`
}

type NotificationPreferences struct {
	TaskReminders  bool `json:"taskReminders"`
	RoutineNudges  bool `json:"routineNudges"`
	FamilyActivity bool `json:"familyActivity"`
	Rewards        bool `json:"rewards"`
	WeeklySummary  bool `json:"weeklySummary"`
	QuietHours     bool `json:"quietHours"`
	PushChannel    bool `json:"pushChannel"`
	EmailChannel   bool `json:"emailChannel"`
}

type PrivacyPreferences struct {
	ActivityVisible bool `json:"activityVisible"`
	Analytics       bool `json:"analytics"`
	Personalization bool `json:"personalization"`
}

func defaultPreferences() Preferences {
	d := preferences.DefaultNotificationSettings()
	return Preferences{
		Theme: "dydan", Appearance: "light", DefaultView: "focus",
		Language: "en", DateFormat: "MM/DD/YYYY", TimeFormat: "h:mm A",
		FirstDayOfWeek: 0, Currency: "USD", ShowItemBadges: true,
		RememberSidebar: true,
		Notifications:   NotificationPreferences{TaskReminders: d.TaskReminders, RoutineNudges: d.RoutineNudges, FamilyActivity: d.FamilyActivity, Rewards: d.Rewards, WeeklySummary: d.WeeklySummary, QuietHours: d.QuietHours, PushChannel: d.PushChannel, EmailChannel: d.EmailChannel},
		Privacy:         PrivacyPreferences{ActivityVisible: true, Analytics: true, Personalization: true},
	}
}

func oneOf(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func validatePreferences(p Preferences) error {
	if !oneOf(p.Theme, "dydan", "sovereign") || !oneOf(p.Appearance, "light", "dark", "system") || !oneOf(p.DefaultView, "focus", "quest") {
		return errInvalidPreferences
	}
	if !oneOf(p.Language, "en", "ar-SA", "fr", "de", "es", "zh-CN", "pt", "ja") || !oneOf(p.DateFormat, "MM/DD/YYYY", "DD/MM/YYYY", "YYYY-MM-DD", "MMMM D, YYYY", "MMM D, YYYY") || !oneOf(p.TimeFormat, "h:mm A", "HH:mm") {
		return errInvalidPreferences
	}
	if p.FirstDayOfWeek < 0 || p.FirstDayOfWeek > 6 || !oneOf(p.Currency, "USD", "GBP", "EUR", "AED", "SAR") {
		return errInvalidPreferences
	}
	return nil
}

var errInvalidPreferences = &preferenceError{}

type preferenceError struct{}

func (*preferenceError) Error() string { return "invalid settings preferences" }

func (h *Handler) loadPreferences(ctx context.Context, userID int) (Preferences, error) {
	stored, err := h.userRepo.GetUserPreferences(ctx, userID)
	if err != nil {
		return Preferences{}, err
	}
	if stored == nil || stored.Value == "" {
		return defaultPreferences(), nil
	}
	preferences := defaultPreferences()
	if err := json.Unmarshal([]byte(stored.Value), &preferences); err != nil {
		return Preferences{}, err
	}
	if err := validatePreferences(preferences); err != nil {
		return Preferences{}, err
	}
	return preferences, nil
}

// verboseRequestLogger emits a structured per-request diagnostic entry when the
// authenticated account has VerboseLogging enabled (Settings > Experimental).
// It runs last in the /users/* chain so it can observe the outcome of the
// request, and the flag itself is read from the same preferences table the
// Settings toggle writes to, so it is enforceable server-side, not client-side.
func (h *Handler) verboseRequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		user, ok := auth.CurrentUser(c)
		if !ok {
			return
		}
		prefs, err := h.loadPreferences(c.Request.Context(), user.ID)
		if err != nil || !prefs.VerboseLogging {
			return
		}
		logging.FromContext(c).Infow("verbose.request",
			"userID", user.ID,
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"bytes", c.Writer.Size(),
			"elapsedMs", float64(time.Since(start).Microseconds())/1000.0,
		)
	}
}

func (h *Handler) getPreferences(c *gin.Context) {
	currentUser := auth.MustCurrentUser(c)
	preferences, err := h.loadPreferences(c.Request.Context(), currentUser.ID)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to load settings preferences"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": preferences})
}

func (h *Handler) updatePreferences(c *gin.Context) {
	currentUser := auth.MustCurrentUser(c)
	var preferences Preferences
	if err := c.ShouldBindJSON(&preferences); err != nil || validatePreferences(preferences) != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": "Invalid settings preferences"})
		return
	}
	encoded, err := json.Marshal(preferences)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to save settings preferences"})
		return
	}
	if err := h.userRepo.SaveUserPreferences(c.Request.Context(), &uModel.UserPreference{UserID: currentUser.ID, Value: string(encoded)}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to save settings preferences"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": preferences})
}

func (h *Handler) resetPreferences(c *gin.Context) {
	currentUser := auth.MustCurrentUser(c)
	preferences := defaultPreferences()
	encoded, _ := json.Marshal(preferences)
	if err := h.userRepo.SaveUserPreferences(c.Request.Context(), &uModel.UserPreference{UserID: currentUser.ID, Value: string(encoded)}); err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Unable to reset settings preferences"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"res": preferences})
}
