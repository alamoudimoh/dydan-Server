package chore

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	chModel "github.com/alamoudimoh/daydan-server/internal/chore/model"
	chRepo "github.com/alamoudimoh/daydan-server/internal/chore/repo"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/daydan-server/internal/circle/repo"
	stModel "github.com/alamoudimoh/daydan-server/internal/subtask/model"
	stgModel "github.com/alamoudimoh/daydan-server/internal/storage/model"
	syncModel "github.com/alamoudimoh/daydan-server/internal/sync/model"
	tModel "github.com/alamoudimoh/daydan-server/internal/thing/model"
	"github.com/alamoudimoh/daydan-server/internal/events"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// newSkipHarness builds a minimal Handler able to exercise SkipChore.
func newSkipHarness(t *testing.T) (*Handler, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(
		&chModel.Chore{}, &chModel.ChoreAssignees{}, &chModel.ChoreHistory{}, &chModel.TimeSession{},
		&stModel.SubTask{}, &syncModel.SyncCursor{}, &tModel.Thing{}, &tModel.ThingChore{}, &stgModel.StorageFile{},
		&cModel.Circle{}, &cModel.UserCircle{},
		&uModel.User{}, &uModel.UserNotificationTarget{},
	); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.WebhookConfig.QueueSize = 10
	cfg.WebhookConfig.Timeout = time.Second
	producer := events.NewEventsProducer(cfg)
	producer.Start(context.Background())

	h := &Handler{
		choreRepo:     chRepo.NewChoreRepository(db, cfg),
		circleRepo:    cRepo.NewCircleRepository(db),
		eventProducer: producer,
	}
	return h, db
}

func skipRequest(userID int, choreID int) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/chores/"+strconv.Itoa(choreID)+"/skip", nil)
	ctx.Set("id", &uModel.UserDetails{User: uModel.User{ID: userID, CircleID: 1}})
	ctx.Params = gin.Params{{Key: "id", Value: strconv.Itoa(choreID)}}
	return ctx, recorder
}

func TestSkipChoreRequiresCompletionCapability(t *testing.T) {
	h, db := newSkipHarness(t)
	// Caller 2 is a plain member; the public chore is assigned only to member 3.
	for _, seed := range []struct {
		id   int
		role cModel.UserRole
	}{
		{2, cModel.UserRoleMember},
		{3, cModel.UserRoleMember},
	} {
		if err := db.Create(&uModel.User{ID: seed.id, Username: "user" + strconv.Itoa(seed.id), CircleID: 1}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&cModel.UserCircle{UserID: seed.id, CircleID: 1, Role: seed.role, IsActive: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	due := time.Now().UTC().Add(-time.Hour)
	chore := chModel.Chore{Name: "laundry", CircleID: 1, CreatedBy: 3, AssignedTo: &[]int{3}[0], IsActive: true, FrequencyType: chModel.FrequencyTypeMonthly, Frequency: 1, AssignStrategy: chModel.AssignmentStrategyRandom, NextDueDate: &due}
	if err := db.Create(&chore).Error; err != nil {
		t.Fatal(err)
	}

	ctx, rec := skipRequest(2, int(chore.ID))
	h.SkipChore(ctx)

	if rec.Code == http.StatusOK {
		t.Fatalf("viewer without completion capability must not skip a chore assigned to another member: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestSkipChoreAllowedForAssignee(t *testing.T) {
	h, db := newSkipHarness(t)
	for _, seed := range []struct {
		id   int
		role cModel.UserRole
	}{
		{3, cModel.UserRoleMember},
	} {
		if err := db.Create(&uModel.User{ID: seed.id, Username: "user" + strconv.Itoa(seed.id), CircleID: 1}).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&cModel.UserCircle{UserID: seed.id, CircleID: 1, Role: seed.role, IsActive: true}).Error; err != nil {
			t.Fatal(err)
		}
	}
	due := time.Now().UTC().Add(-time.Hour)
	chore := chModel.Chore{Name: "laundry", CircleID: 1, CreatedBy: 3, AssignedTo: &[]int{3}[0], IsActive: true, FrequencyType: chModel.FrequencyTypeMonthly, Frequency: 1, AssignStrategy: chModel.AssignmentStrategyRandom, NextDueDate: &due}
	if err := db.Create(&chore).Error; err != nil {
		t.Fatal(err)
	}

	ctx, rec := skipRequest(3, int(chore.ID))
	h.SkipChore(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("assignee should be able to skip their chore: status=%d body=%s", rec.Code, rec.Body.String())
	}
}
