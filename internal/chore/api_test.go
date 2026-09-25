package chore

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	chRepo "github.com/alamoudimoh/dydan-server/internal/chore/repo"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/dydan-server/internal/circle/repo"
	"github.com/alamoudimoh/dydan-server/internal/events"
	nModel "github.com/alamoudimoh/dydan-server/internal/notifier/model"
	nRepo "github.com/alamoudimoh/dydan-server/internal/notifier/repo"
	nps "github.com/alamoudimoh/dydan-server/internal/notifier/service"
	stgModel "github.com/alamoudimoh/dydan-server/internal/storage/model"
	stModel "github.com/alamoudimoh/dydan-server/internal/subtask/model"
	syncModel "github.com/alamoudimoh/dydan-server/internal/sync/model"
	tModel "github.com/alamoudimoh/dydan-server/internal/thing/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type eapiHarness struct {
	db     *gorm.DB
	api    *API
	router *gin.Engine
}

func newEAPIHarness(t *testing.T) *eapiHarness {
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
		&nModel.Notification{},
	); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.WebhookConfig.QueueSize = 10
	cfg.WebhookConfig.Timeout = time.Second

	choreRepo := chRepo.NewChoreRepository(db, cfg)
	userRepo := uRepo.NewUserRepository(db, cfg)
	circleRepo := cRepo.NewCircleRepository(db)
	nPlanner := nps.NewNotificationPlanner(nRepo.NewNotificationRepository(db), circleRepo)
	producer := events.NewEventsProducer(cfg)
	producer.Start(context.Background())

	api := NewAPI(choreRepo, userRepo, circleRepo, nPlanner, nRepo.NewNotificationRepository(db), nil, producer, nil)
	return &eapiHarness{db: db, api: api}
}

func eapiContext(userID, circleID int, req *http.Request) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = req
	ctx.Set("id", &uModel.UserDetails{User: uModel.User{ID: userID, CircleID: circleID}})
	return ctx, recorder
}

func seedCircleMember(t *testing.T, db *gorm.DB, userID int, role cModel.UserRole) {
	t.Helper()
	if err := db.Create(&uModel.User{ID: userID, Username: "user" + strconv.Itoa(userID), CircleID: 1}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.UserCircle{UserID: userID, CircleID: 1, Role: role, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestEAPICompleteChoreRejectsPerformerSubstitutionByNonAdmin(t *testing.T) {
	h := newEAPIHarness(t)
	// Caller 2 is a plain member; chore is assigned ONLY to member 3.
	seedCircleMember(t, h.db, 2, cModel.UserRoleMember)
	seedCircleMember(t, h.db, 3, cModel.UserRoleMember)
	due := time.Now().UTC().Add(-time.Hour)
	chore := chModel.Chore{Name: "vacuum", CircleID: 1, CreatedBy: 3, AssignedTo: &[]int{3}[0], IsActive: true, FrequencyType: chModel.FrequencyTypeOnce, AssignStrategy: chModel.AssignmentStrategyRandom, NextDueDate: &due}
	if err := h.db.Create(&chore).Error; err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/eapi/v1/chore/"+strconv.Itoa(int(chore.ID))+"/complete?completedBy=3", nil)
	ctx, rec := eapiContext(2, 1, req)
	ctx.Params = gin.Params{{Key: "id", Value: strconv.Itoa(int(chore.ID))}}
	h.api.CompleteChore(ctx)

	if rec.Code == http.StatusOK {
		t.Fatalf("non-assigned caller must not complete a chore by naming an assignee as completedBy: status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestEAPICompleteChoreAdminDelegationStillAllowed(t *testing.T) {
	h := newEAPIHarness(t)
	// Caller 2 is a circle admin completing on behalf of assignee 3.
	seedCircleMember(t, h.db, 2, cModel.UserRoleAdmin)
	seedCircleMember(t, h.db, 3, cModel.UserRoleMember)
	due := time.Now().UTC().Add(-time.Hour)
	chore := chModel.Chore{Name: "dishes", CircleID: 1, CreatedBy: 3, AssignedTo: &[]int{3}[0], IsActive: true, FrequencyType: chModel.FrequencyTypeOnce, AssignStrategy: chModel.AssignmentStrategyRandom, NextDueDate: &due}
	if err := h.db.Create(&chore).Error; err != nil {
		t.Fatal(err)
	}

	req := httptest.NewRequest(http.MethodPost, "/eapi/v1/chore/"+strconv.Itoa(int(chore.ID))+"/complete?completedBy=3", nil)
	ctx, rec := eapiContext(2, 1, req)
	ctx.Params = gin.Params{{Key: "id", Value: strconv.Itoa(int(chore.ID))}}
	h.api.CompleteChore(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("admin delegation via completedBy should still work: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var history []chModel.ChoreHistory
	if err := h.db.Where("chore_id = ?", chore.ID).Find(&history).Error; err != nil {
		t.Fatal(err)
	}
	if len(history) != 1 || history[0].CompletedBy != 3 {
		t.Fatalf("completion must be attributed to member 3, got %#v", history)
	}
}

func TestEAPICreateChoreDerivesCreatedByForNonAdmin(t *testing.T) {
	h := newEAPIHarness(t)
	seedCircleMember(t, h.db, 2, cModel.UserRoleMember)
	seedCircleMember(t, h.db, 3, cModel.UserRoleMember)

	body := []byte(`{"name":"sweep","createdBy":3}`)
	req := httptest.NewRequest(http.MethodPost, "/eapi/v1/chore", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx, rec := eapiContext(2, 1, req)
	h.api.CreateChore(ctx)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created chModel.Chore
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.CreatedBy != 2 {
		t.Fatalf("plain member must not attribute chore creation to another member: createdBy=%d", created.CreatedBy)
	}
}

func TestEAPICreateChoreAdminAttributionAllowed(t *testing.T) {
	h := newEAPIHarness(t)
	seedCircleMember(t, h.db, 2, cModel.UserRoleAdmin)
	seedCircleMember(t, h.db, 3, cModel.UserRoleMember)

	body := []byte(`{"name":"sweep","createdBy":3}`)
	req := httptest.NewRequest(http.MethodPost, "/eapi/v1/chore", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ctx, rec := eapiContext(2, 1, req)
	h.api.CreateChore(ctx)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	var created chModel.Chore
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.CreatedBy != 3 {
		t.Fatalf("admin attribution to circle member should be honored: createdBy=%d", created.CreatedBy)
	}
}
