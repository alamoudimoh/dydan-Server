package user

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"donetick.com/core/internal/auth"
	cModel "donetick.com/core/internal/circle/model"
	cRepo "donetick.com/core/internal/circle/repo"
	uModel "donetick.com/core/internal/user/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSettingsPermissionMiddlewareUsesEffectiveRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&cModel.UserCircle{}); err != nil {
		t.Fatal(err)
	}
	handler := &Handler{circleRepo: cRepo.NewCircleRepository(db)}
	tests := []struct {
		name     string
		userType uModel.UserType
		role     cModel.UserRole
		active   bool
		want     int
	}{
		{name: "adult member may manage own account", userType: uModel.UserTypeParent, role: cModel.UserRoleMember, active: true, want: http.StatusNoContent},
		{name: "managed child cannot inherit admin", userType: uModel.UserTypeChild, role: cModel.UserRoleAdmin, active: true, want: http.StatusForbidden},
		{name: "inactive member is forbidden", userType: uModel.UserTypeParent, role: cModel.UserRoleAdmin, active: false, want: http.StatusForbidden},
	}
	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			userID := index + 1
			membership := cModel.UserCircle{UserID: userID, CircleID: 10, Role: tt.role, IsActive: tt.active}
			if err := db.Create(&membership).Error; err != nil {
				t.Fatal(err)
			}
			user := &uModel.UserDetails{User: uModel.User{ID: userID, CircleID: 10, UserType: tt.userType}}
			router := gin.New()
			router.GET("/", func(c *gin.Context) {
				c.Set("id", user)
				c.Next()
			}, handler.requirePermission(auth.PermissionManageAccount), func(c *gin.Context) {
				c.Status(http.StatusNoContent)
			})
			response := httptest.NewRecorder()
			router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/", nil))
			if response.Code != tt.want {
				t.Fatalf("status = %d, want %d", response.Code, tt.want)
			}
		})
	}
}
