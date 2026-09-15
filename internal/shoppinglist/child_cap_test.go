package shoppinglist

import (
	"net/http"
	"net/http/httptest"
	"testing"

	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/dydan-server/internal/circle/repo"
	slModel "github.com/alamoudimoh/dydan-server/internal/shoppinglist/model"
	slRepo "github.com/alamoudimoh/dydan-server/internal/shoppinglist/repo"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// A managed child account with a stale admin membership row must not gain
// circle-list management through the shopping-list module.
func TestChildAccountCannotManageCircleLists(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&slModel.ShoppingList{}, &slModel.ShoppingListItem{}, &cModel.UserCircle{}, &cModel.Circle{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.UserCircle{UserID: 5, CircleID: 3, Role: cModel.UserRoleAdmin, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	h := NewHandler(slRepo.NewRepository(db), cRepo.NewCircleRepository(db))

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c.Set("id", &uModel.UserDetails{User: uModel.User{ID: 5, CircleID: 3, UserType: uModel.UserTypeChild}})

	role, active := h.membership(c, 3, 5)
	if !active {
		t.Fatal("child membership should be active")
	}
	if h.isCircleAdmin(role) {
		t.Fatalf("child with stale admin row must be capped to member, got %q", role)
	}

	// Control: the same row for an adult principal still confers admin.
	c2, _ := gin.CreateTestContext(httptest.NewRecorder())
	c2.Request = httptest.NewRequest(http.MethodGet, "/", nil)
	c2.Set("id", &uModel.UserDetails{User: uModel.User{ID: 6, CircleID: 3}})
	if err := db.Create(&cModel.UserCircle{UserID: 6, CircleID: 3, Role: cModel.UserRoleAdmin, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	role2, _ := h.membership(c2, 3, 6)
	if !h.isCircleAdmin(role2) {
		t.Fatalf("adult admin must keep admin role, got %q", role2)
	}
}
