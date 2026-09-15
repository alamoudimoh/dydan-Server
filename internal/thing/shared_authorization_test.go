package thing

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alamoudimoh/dydan-server/config"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/dydan-server/internal/circle/repo"
	lModel "github.com/alamoudimoh/dydan-server/internal/label/model"
	tModel "github.com/alamoudimoh/dydan-server/internal/thing/model"
	tRepo "github.com/alamoudimoh/dydan-server/internal/thing/repo"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestSharedThingOwnerNeedsEffectiveMaintenanceRole(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &cModel.Circle{}, &cModel.UserCircle{}, &lModel.Label{}, &tModel.Thing{}, &tModel.ThingChore{}); err != nil {
		t.Fatal(err)
	}
	config := &config.Config{Database: config.DatabaseConfig{Type: "sqlite"}}
	h := NewHandler(nil, cRepo.NewCircleRepository(db), nil, nil, tRepo.NewThingRepository(db, config), nil)
	owner := uModel.User{ID: 1, Username: "owner", CircleID: 5}
	if err := db.Create(&owner).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.Circle{ID: 5, Name: "Home", CreatedBy: owner.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.UserCircle{UserID: owner.ID, CircleID: 5, Role: cModel.UserRoleManager, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	shared := tModel.Thing{ID: 10, UserID: owner.ID, CircleID: 5, IsShared: true, Name: "Shared car", Type: "text"}
	personal := tModel.Thing{ID: 11, UserID: owner.ID, CircleID: 5, Name: "Personal tracker", Type: "text"}
	if err := db.Create(&[]tModel.Thing{shared, personal}).Error; err != nil {
		t.Fatal(err)
	}

	request := func(user uModel.User, req ThingRequest) *httptest.ResponseRecorder {
		body, err := json.Marshal(req)
		if err != nil {
			t.Fatal(err)
		}
		response := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(response)
		ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/things", bytes.NewReader(body))
		ctx.Request.Header.Set("Content-Type", "application/json")
		ctx.Set("id", &uModel.UserDetails{User: user})
		h.UpdateThing(ctx)
		return response
	}

	child := owner
	child.UserType = uModel.UserTypeChild
	if response := request(child, ThingRequest{ID: shared.ID, Name: "Changed shared car", Type: "text"}); response.Code != http.StatusForbidden {
		t.Fatalf("managed child bypassed shared maintenance asset gate: %d %s", response.Code, response.Body.String())
	}
	if response := request(child, ThingRequest{ID: personal.ID, Name: "Changed personal tracker", Type: "text"}); response.Code != http.StatusOK {
		t.Fatalf("managed child lost own personal Thing access: %d %s", response.Code, response.Body.String())
	}
	if response := request(owner, ThingRequest{ID: shared.ID, Name: "Changed shared car", Type: "text"}); response.Code != http.StatusOK {
		t.Fatalf("adult manager lost shared maintenance asset access: %d %s", response.Code, response.Body.String())
	}
}
