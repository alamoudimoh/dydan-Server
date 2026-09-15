package circle

import (
	"bytes"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alamoudimoh/dydan-server/config"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/dydan-server/internal/circle/repo"
	"github.com/alamoudimoh/dydan-server/internal/storage"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type roleHarness struct {
	handler  *Handler
	db       *gorm.DB
	circleID int
}

func familyRoleTestSetup(t *testing.T) *roleHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := &config.Config{}
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserNotificationTarget{}, &cModel.Circle{}, &cModel.UserCircle{}); err != nil {
		t.Fatal(err)
	}
	circle := cModel.Circle{Name: "Family", InviteCode: "invite"}
	if err := db.Create(&circle).Error; err != nil {
		t.Fatal(err)
	}
	parent := uModel.User{ID: 1, Username: "parent", CircleID: int(circle.ID), UserType: uModel.UserTypeParent}
	child := uModel.User{ID: 2, Username: "parent_bobby", CircleID: int(circle.ID), UserType: uModel.UserTypeChild}
	for _, u := range []uModel.User{parent, child} {
		if err := db.Create(&u).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Create(&cModel.UserCircle{UserID: 1, CircleID: int(circle.ID), Role: cModel.UserRoleAdmin, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.UserCircle{UserID: 2, CircleID: int(circle.ID), Role: cModel.UserRoleMember, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(cRepo.NewCircleRepository(db), uRepo.NewUserRepository(db, cfg), nil, nil, storage.NewURLSignerLocal(cfg), cfg)
	return &roleHarness{handler: handler, db: db, circleID: int(circle.ID)}
}

func TestChangeMemberRoleCannotElevateChildAccounts(t *testing.T) {
	h := familyRoleTestSetup(t)

	for _, role := range []cModel.Role{cModel.RoleAdmin, cModel.RoleManager} {
		body := fmt.Sprintf(`{"memberId":2,"role":"%s"}`, role)
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/circles/members/role", bytes.NewBufferString(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		ctx.Set("id", &uModel.UserDetails{User: uModel.User{ID: 1, CircleID: h.circleID, UserType: uModel.UserTypeParent}})
		h.handler.ChangeMemberRole(ctx)

		if recorder.Code == http.StatusOK {
			t.Fatalf("promoting a child account to %q must be rejected", role)
		}
		var membership cModel.UserCircle
		if err := h.db.Where("user_id = ? AND circle_id = ?", 2, h.circleID).First(&membership).Error; err != nil {
			t.Fatal(err)
		}
		if string(membership.Role) == string(role) {
			t.Fatalf("child membership must not be changed to %q", role)
		}
	}
}
