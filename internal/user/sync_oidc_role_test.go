package user

import (
	"net/http/httptest"
	"testing"

	"github.com/alamoudimoh/daydan-server/config"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/daydan-server/internal/circle/repo"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// OIDC group-based role sync must not be able to promote a managed child
// account to admin or manager, bypassing the interactive role-change guard.
func TestSyncOIDCRoleRefusesToElevateChildAccounts(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserNotificationTarget{}, &cModel.Circle{}, &cModel.UserCircle{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.Circle{ID: 1, Name: "circle"}).Error; err != nil {
		t.Fatal(err)
	}
	child := uModel.User{ID: 2, Username: "parent_kid", CircleID: 1, UserType: uModel.UserTypeChild}
	if err := db.Create(&child).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.UserCircle{UserID: 2, CircleID: 1, Role: cModel.UserRoleMember, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}

	cfg := &config.Config{}
	cfg.OAuth2Config.AdminGroups = []string{"daydan-admins"}
	cfg.OAuth2Config.ManagerGroups = []string{"daydan-managers"}
	h := &Handler{
		userRepo:     uRepo.NewUserRepository(db, cfg),
		circleRepo:   cRepo.NewCircleRepository(db),
		oauth2Config: cfg.OAuth2Config,
	}
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = httptest.NewRequest("POST", "/", nil)

	for _, role := range []struct {
		groups []string
		name   string
	}{
		{[]string{"daydan-admins"}, string(cModel.UserRoleAdmin)},
		{[]string{"daydan-managers"}, string(cModel.UserRoleManager)},
	} {
		h.syncOIDCRole(ctx, 2, 1, role.groups)
		var membership cModel.UserCircle
		if err := db.Where("user_id = ? AND circle_id = ?", 2, 1).First(&membership).Error; err != nil {
			t.Fatal(err)
		}
		if membership.Role == cModel.UserRole(role.name) {
			t.Fatalf("OIDC role sync must not elevate a managed child to %q", role.name)
		}
	}

	// Control: an adult principal IS elevated by the same groups.
	adult := uModel.User{ID: 3, Username: "adult", CircleID: 1, UserType: uModel.UserTypeParent, Provider: uModel.AuthProviderOAuth2}
	if err := db.Create(&adult).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.UserCircle{UserID: 3, CircleID: 1, Role: cModel.UserRoleMember, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	h.syncOIDCRole(ctx, 3, 1, []string{"daydan-admins"})
	var membership cModel.UserCircle
	if err := db.Where("user_id = ? AND circle_id = ?", 3, 1).First(&membership).Error; err != nil {
		t.Fatal(err)
	}
	if membership.Role != cModel.UserRoleAdmin {
		t.Fatalf("adult OIDC admin-group member should still be promoted, got %q", membership.Role)
	}
}
