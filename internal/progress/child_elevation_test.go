package progress

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	chModel "github.com/alamoudimoh/daydan-server/internal/chore/model"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/daydan-server/internal/circle/repo"
	lModel "github.com/alamoudimoh/daydan-server/internal/label/model"
	tModel "github.com/alamoudimoh/daydan-server/internal/thing/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// A managed child account must never gain family-wide analytics or goal
// management, even when a stale membership row carries an elevated role.
func TestChildAccountCannotExerciseProgressManagerCapabilities(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&chModel.Chore{}, &chModel.ChoreAssignees{}, &cModel.UserCircle{}, &lModel.Label{}, &tModel.ThingChore{}, &uModel.User{}); err != nil {
		t.Fatal(err)
	}
	h := &Handler{db: db, circles: cRepo.NewCircleRepository(db)}

	child := uModel.User{ID: 5, Username: "parent_kid", CircleID: 30, UserType: uModel.UserTypeChild}
	if err := db.Create(&child).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.UserCircle{UserID: 5, CircleID: 30, Role: cModel.UserRoleAdmin, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}

	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/api/v1/progress/workload", nil)
	ctx.Set("id", &uModel.UserDetails{User: child})
	h.workload(ctx)

	if recorder.Code != http.StatusOK {
		t.Fatalf("workload status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var out struct {
		CanViewFamily bool `json:"canViewFamily"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if out.CanViewFamily {
		t.Fatal("managed child must not receive family-wide visibility despite stale admin role")
	}
}
