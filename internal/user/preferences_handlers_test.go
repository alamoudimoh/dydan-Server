package user

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alamoudimoh/dydan-server/config"
	auth "github.com/alamoudimoh/dydan-server/internal/auth"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestPreferencesPersistAndReset(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserPreference{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	user := &uModel.User{Username: "preferences-user", Email: "preferences@example.test"}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	h := &Handler{userRepo: uRepo.NewUserRepository(db, &config.Config{})}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		auth.SetCurrentUser(c, &uModel.UserDetails{User: *user})
		c.Next()
	})
	r.GET("/preferences", h.getPreferences)
	r.PUT("/preferences", h.updatePreferences)
	r.POST("/preferences/reset", h.resetPreferences)

	updated := `{"theme":"sovereign","appearance":"dark","defaultView":"quest","compactMode":true,"language":"ar-SA","dateFormat":"DD/MM/YYYY","timeFormat":"HH:mm","firstDayOfWeek":6,"currency":"SAR","sidebarCollapsed":true,"showItemBadges":false,"rememberSidebar":false,"notifications":{"taskReminders":false,"routineNudges":true,"familyActivity":true,"rewards":false,"weeklySummary":true,"quietHours":true,"pushChannel":false,"emailChannel":false},"privacy":{"activityVisible":false,"analytics":false,"personalization":true},"betaFeatures":true,"verboseLogging":true}`
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPut, "/preferences", strings.NewReader(updated))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("save preferences: %d (%s)", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/preferences", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"theme":"sovereign"`) || !strings.Contains(w.Body.String(), `"compactMode":true`) {
		t.Fatalf("stored preferences missing: %d (%s)", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/preferences/reset", nil))
	if w.Code != http.StatusOK || !strings.Contains(w.Body.String(), `"theme":"dydan"`) || !strings.Contains(w.Body.String(), `"compactMode":false`) {
		t.Fatalf("reset preferences: %d (%s)", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPut, "/preferences", strings.NewReader(`{"theme":"untrusted"}`))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid preferences should be rejected: %d (%s)", w.Code, w.Body.String())
	}
}
