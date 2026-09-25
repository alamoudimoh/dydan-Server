package progress

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/internal/auth"
	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/dydan-server/internal/circle/repo"
	lModel "github.com/alamoudimoh/dydan-server/internal/label/model"
	pModel "github.com/alamoudimoh/dydan-server/internal/points"
	rModel "github.com/alamoudimoh/dydan-server/internal/reward"
	tModel "github.com/alamoudimoh/dydan-server/internal/thing/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type reportFixture struct {
	db     *gorm.DB
	circle cModel.Circle
	user   uModel.User
}

func setupReportFixture(t *testing.T, role cModel.UserRole, seed func(db *gorm.DB, fx *reportFixture)) (*reportFixture, *Handler) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserPreference{}, &cModel.Circle{}, &cModel.UserCircle{}, &chModel.Chore{}, &chModel.ChoreAssignees{}, &chModel.ChoreHistory{}, &lModel.Label{}, &tModel.ThingChore{}, &pModel.PointsHistory{}, &rModel.RewardRequest{}, &Goal{}); err != nil {
		t.Fatal(err)
	}
	fx := &reportFixture{db: db}
	fx.circle = cModel.Circle{Name: "Fresh Family", InviteCode: "FRESH"}
	if err := db.Create(&fx.circle).Error; err != nil {
		t.Fatal(err)
	}
	fx.user = uModel.User{Username: "freshparent", DisplayName: "Fresh Parent", Email: "fresh+r@test.local", CircleID: fx.circle.ID, Timezone: "UTC"}
	if err := db.Create(&fx.user).Error; err != nil {
		t.Fatal(err)
	}
	member := cModel.UserCircle{UserID: fx.user.ID, CircleID: fx.circle.ID, Role: role, IsActive: true}
	if err := db.Create(&member).Error; err != nil {
		t.Fatal(err)
	}
	if seed != nil {
		seed(db, fx)
	}
	return fx, NewHandler(db, cRepo.NewCircleRepository(db))
}

func TestReportActivityVisibilityFiltersOtherMemberButNotSelf(t *testing.T) {
	fx, h := setupReportFixture(t, cModel.UserRoleAdmin, func(db *gorm.DB, fx *reportFixture) {
		peer := uModel.User{Username: "hidden-peer", Email: "hidden@example.test", CircleID: fx.circle.ID, Timezone: "UTC"}
		if err := db.Create(&peer).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&cModel.UserCircle{UserID: peer.ID, CircleID: fx.circle.ID, Role: cModel.UserRoleMember, IsActive: true}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&uModel.UserPreference{UserID: peer.ID, Value: `{"privacy":{"activityVisible":false}}`}).Error; err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		chore := chModel.Chore{Name: "hidden", CircleID: fx.circle.ID, CreatedBy: peer.ID, AssignedTo: &peer.ID, IsActive: true}
		if err := db.Create(&chore).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&chModel.ChoreHistory{ChoreID: chore.ID, CompletedBy: peer.ID, AssignedTo: &peer.ID, PerformedAt: &now, Status: chModel.ChoreHistoryStatusCompleted}).Error; err != nil {
			t.Fatal(err)
		}
	})
	body := runReport(t, fx, h)
	if body["completed"].(float64) != 0 || len(body["members"].([]any)) != 0 {
		t.Fatalf("hidden member leaked into report: %v", body)
	}
}

func runReport(t *testing.T, fx *reportFixture, h *Handler) map[string]any {
	t.Helper()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest("GET", "/api/v1/progress/reports?range=week", nil)
	auth.SetCurrentUser(c, &uModel.UserDetails{User: fx.user})

	h.report(c)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body
}

func TestReportForAccountWithoutActivityIsEmptyNotError(t *testing.T) {
	fx, h := setupReportFixture(t, cModel.UserRoleAdmin, nil)
	body := runReport(t, fx, h)

	if body["completed"].(float64) != 0 || body["overdue"].(float64) != 0 {
		t.Fatalf("expected empty report, got: %v", body)
	}
	if len(body["categories"].([]any)) != 0 || len(body["members"].([]any)) != 0 {
		t.Fatalf("expected empty report, got: %v", body)
	}
}

func TestReportForEmptyMemberAccountIsEmpty(t *testing.T) {
	fx, h := setupReportFixture(t, cModel.UserRoleMember, nil)
	body := runReport(t, fx, h)

	if body["completed"].(float64) != 0 || body["canViewFamily"] != false {
		t.Fatalf("expected empty member report, got: %v", body)
	}
	if len(body["categories"].([]any)) != 0 || len(body["members"].([]any)) != 0 {
		t.Fatalf("expected empty member report, got: %v", body)
	}
}

func TestReportForAccountWithActivityReturnsSummaries(t *testing.T) {
	fx, h := setupReportFixture(t, cModel.UserRoleAdmin, func(db *gorm.DB, fx *reportFixture) {
		performed := time.Now().AddDate(0, 0, -2)
		label := lModel.Label{Name: "Chores", CircleID: &fx.circle.ID, CreatedBy: fx.user.ID, IsCategory: true}
		if err := db.Create(&label).Error; err != nil {
			t.Fatal(err)
		}
		chore := chModel.Chore{Name: "Take out trash", CategoryID: &label.ID, CircleID: fx.circle.ID, CreatedBy: fx.user.ID, UpdatedBy: fx.user.ID, IsActive: true, AssignedTo: &fx.user.ID}
		if err := db.Create(&chore).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&chModel.ChoreHistory{ChoreID: chore.ID, PerformedAt: &performed, CompletedBy: fx.user.ID, AssignedTo: &fx.user.ID, Status: chModel.ChoreHistoryStatusCompleted}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&tModel.ThingChore{ThingID: 1, ChoreID: chore.ID, TriggerState: "done", Condition: ""}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&pModel.PointsHistory{Action: pModel.PointsHistoryActionAdd, Points: 100, CreatedAt: performed, CreatedBy: fx.user.ID, UserID: fx.user.ID, CircleID: fx.circle.ID}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&pModel.PointsHistory{Action: pModel.PointsHistoryActionRedeem, Points: -30, CreatedAt: performed, CreatedBy: fx.user.ID, UserID: fx.user.ID, CircleID: fx.circle.ID}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&rModel.RewardRequest{CircleID: fx.circle.ID, RewardID: 1, UserID: fx.user.ID, RewardNameSnapshot: "Movie ticket", CostPointsSnapshot: 30, Status: rModel.RequestStatusPending}).Error; err != nil {
			t.Fatal(err)
		}
	})

	body := runReport(t, fx, h)

	if body["completed"].(float64) != 1 {
		t.Fatalf("expected 1 completed task, got: %v", body)
	}
	categories := body["categories"].([]any)
	if len(categories) != 1 || categories[0].(map[string]any)["name"] != "Chores" || categories[0].(map[string]any)["count"].(float64) != 1 {
		t.Fatalf("expected single category, got: %v", body)
	}
	members := body["members"].([]any)
	if len(members) != 1 || members[0].(map[string]any)["name"] != "Fresh Parent" || members[0].(map[string]any)["count"].(float64) != 1 {
		t.Fatalf("expected single member, got: %v", body)
	}
	points := body["points"].(map[string]any)
	if points["earned"].(float64) != 100 || points["redeemed"].(float64) != 30 {
		t.Fatalf("expected points earned=100 redeemed=30, got: %v", body)
	}
	if len(body["rewards"].([]any)) != 1 {
		t.Fatalf("expected 1 reward request, got: %v", body)
	}
	maintenance := body["maintenance"].(map[string]any)
	if maintenance["completed"].(float64) != 1 {
		t.Fatalf("expected 1 maintenance completed, got: %v", body)
	}
}
