package routine

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	mModel "github.com/alamoudimoh/dydan-server/internal/meter/model"
	pModel "github.com/alamoudimoh/dydan-server/internal/points"
	stModel "github.com/alamoudimoh/dydan-server/internal/subtask/model"
	syncModel "github.com/alamoudimoh/dydan-server/internal/sync/model"
	tModel "github.com/alamoudimoh/dydan-server/internal/thing/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

type routineHarness struct {
	db      *gorm.DB
	handler *Handler
	userID  int
	circle  int
}

func newRoutineHarness(t *testing.T) *routineHarness {
	t.Helper()
	gin.SetMode(gin.TestMode)
	dsn := fmt.Sprintf("file:routine-%d?mode=memory&cache=shared", time.Now().UnixNano())
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&Pack{}, &Item{}, &Application{}, &chModel.Chore{}, &chModel.ChoreAssignees{}, &chModel.ChoreLabels{}, &chModel.ChoreHistory{}, &cModel.Circle{}, &cModel.UserCircle{}, &uModel.User{}, &tModel.Thing{}, &tModel.ThingChore{}, &mModel.Meter{}, &mModel.ChoreSchedule{}, &stModel.SubTask{}, &syncModel.SyncCursor{}, &pModel.PointsHistory{}); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	user := uModel.User{Username: "routine-owner", CircleID: 1, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	circle := cModel.Circle{ID: 1, Name: "Home", CreatedBy: user.ID, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&circle).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.UserCircle{UserID: user.ID, CircleID: circle.ID, Role: cModel.UserRoleAdmin, IsActive: true, CreatedAt: now, UpdatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Database: config.DatabaseConfig{Type: "sqlite"}}
	chores := chRepo.NewChoreRepository(db, cfg)
	return &routineHarness{db: db, handler: NewHandler(db, chores, cRepo.NewCircleRepository(db)), userID: user.ID, circle: circle.ID}
}

func (h *routineHarness) context(method, target string, body any, params gin.Params) (*gin.Context, *httptest.ResponseRecorder) {
	if target == "" {
		target = "/"
	}
	var raw []byte
	if body != nil {
		raw, _ = json.Marshal(body)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, target, bytes.NewReader(raw))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = params
	ctx.Set("id", &uModel.UserDetails{User: uModel.User{ID: h.userID, CircleID: h.circle}})
	return ctx, recorder
}

func createPack(t *testing.T, h *routineHarness, items []Item) Pack {
	t.Helper()
	now := time.Now().UTC()
	pack := Pack{CircleID: h.circle, Name: "Weekly Cleaning", Description: "House reset", IsActive: true, CreatedBy: h.userID, CreatedAt: now, UpdatedAt: now}
	if err := h.db.Create(&pack).Error; err != nil {
		t.Fatal(err)
	}
	for index := range items {
		items[index].PackID = pack.ID
		items[index].Position = index
		if err := normalizeItem(&items[index]); err != nil {
			t.Fatal(err)
		}
		if err := h.db.Create(&items[index]).Error; err != nil {
			t.Fatal(err)
		}
	}
	pack.Items = items
	return pack
}

func applyPack(t *testing.T, h *routineHarness, packID int, key string, selections map[int]int) (*httptest.ResponseRecorder, Application) {
	t.Helper()
	ctx, response := h.context(http.MethodPost, "/api/v1/routines/"+strconv.Itoa(packID)+"/apply", applyInput{StartDate: "2030-11-20", IdempotencyKey: key, AssetSelections: selections}, gin.Params{{Key: "id", Value: strconv.Itoa(packID)}})
	h.handler.Apply(ctx)
	var payload struct {
		Res Application `json:"res"`
	}
	_ = json.Unmarshal(response.Body.Bytes(), &payload)
	return response, payload.Res
}

func TestRoutineCRUDReorderArchiveAndCircleIsolation(t *testing.T) {
	h := newRoutineHarness(t)
	ctx, response := h.context(http.MethodPost, "/api/v1/routines", packInput{Name: "Travel Preparation", Description: "Before leaving"}, nil)
	h.handler.Create(ctx)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body)
	}
	var created struct {
		Res Pack `json:"res"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Passports", "Chargers", "Windows"} {
		ctx, response = h.context(http.MethodPost, "", Item{Name: name, FrequencyType: chModel.FrequencyTypeOnce, AssignStrategy: chModel.AssignmentStrategyNoAssignee}, gin.Params{{Key: "id", Value: strconv.Itoa(created.Res.ID)}})
		h.handler.AddItem(ctx)
		if response.Code != http.StatusCreated {
			t.Fatalf("add status=%d body=%s", response.Code, response.Body)
		}
	}
	pack, err := h.handler.pack(ctx, created.Res.ID, h.circle)
	if err != nil {
		t.Fatal(err)
	}
	order := []int{pack.Items[2].ID, pack.Items[0].ID, pack.Items[1].ID}
	ctx, response = h.context(http.MethodPut, "", map[string]any{"itemIds": order}, gin.Params{{Key: "id", Value: strconv.Itoa(pack.ID)}})
	h.handler.Reorder(ctx)
	if response.Code != http.StatusOK {
		t.Fatalf("reorder status=%d body=%s", response.Code, response.Body)
	}
	pack, _ = h.handler.pack(ctx, pack.ID, h.circle)
	if pack.Items[0].ID != order[0] || pack.Items[2].ID != order[2] {
		t.Fatalf("unexpected order: %#v", pack.Items)
	}
	ctx, response = h.context(http.MethodPatch, "", map[string]any{"isActive": false}, gin.Params{{Key: "id", Value: strconv.Itoa(pack.ID)}})
	h.handler.SetActive(ctx)
	if response.Code != http.StatusOK {
		t.Fatalf("archive status=%d body=%s", response.Code, response.Body)
	}
	ctx, response = h.context(http.MethodPatch, "", map[string]any{"isActive": true}, gin.Params{{Key: "id", Value: strconv.Itoa(pack.ID)}})
	h.handler.SetActive(ctx)
	if response.Code != http.StatusOK {
		t.Fatalf("reactivate status=%d body=%s", response.Code, response.Body)
	}

	h.circle = 2
	ctx, response = h.context(http.MethodGet, "", nil, gin.Params{{Key: "id", Value: strconv.Itoa(pack.ID)}})
	h.handler.Read(ctx)
	if response.Code != http.StatusForbidden {
		t.Fatalf("non-member cross-circle read status=%d body=%s", response.Code, response.Body)
	}
}

func TestManagedChildWithStaleManagerRoleCannotManageAnotherRoutine(t *testing.T) {
	h := newRoutineHarness(t)
	pack := createPack(t, h, nil)
	// The authenticated principal carries the authoritative managed-child type.
	// Seed the historical membership directly without invoking child-account
	// creation, which correctly normalizes it to member.
	child := uModel.User{ID: 999, CircleID: h.circle, UserType: uModel.UserTypeChild}
	if err := h.db.Exec("INSERT INTO users (id, username, circle_id, user_type) VALUES (?, ?, ?, ?)", child.ID, "child", child.CircleID, child.UserType).Error; err != nil {
		t.Fatalf("seed child: %v", err)
	}
	if err := h.db.Create(&cModel.UserCircle{UserID: child.ID, CircleID: h.circle, Role: cModel.UserRoleManager, IsActive: true}).Error; err != nil {
		t.Fatalf("seed child membership: %v", err)
	}

	body, err := json.Marshal(packInput{Name: "Changed", Description: "must not save"})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(response)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/api/v1/routines/"+strconv.Itoa(pack.ID), bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Params = gin.Params{{Key: "id", Value: strconv.Itoa(pack.ID)}}
	ctx.Set("id", &uModel.UserDetails{User: child})
	h.handler.Update(ctx)
	if response.Code != http.StatusForbidden {
		t.Fatalf("managed child with stale manager role managed another routine: %d %s", response.Code, response.Body.String())
	}
}

func TestApplyIsAtomicIdempotentIndependentAndPreservesConfiguration(t *testing.T) {
	h := newRoutineHarness(t)
	points := 5
	weekly := Item{Name: "Bathrooms", FrequencyType: chModel.FrequencyTypeWeekly, Frequency: 1, AssignStrategy: chModel.AssignmentStrategyRoundRobin, AssigneeIDs: []int{h.userID}, AssignedTo: &h.userID, Points: &points, RequireApproval: true}
	daily := Item{Name: "Trash", FrequencyType: chModel.FrequencyTypeDaily, Frequency: 1, AssignStrategy: chModel.AssignmentStrategyNoAssignee}
	once := Item{Name: "Windows", FrequencyType: chModel.FrequencyTypeOnce, Frequency: 1, AssignStrategy: chModel.AssignmentStrategyNoAssignee}
	pack := createPack(t, h, []Item{weekly, daily, once})
	response, receipt := applyPack(t, h, pack.ID, "apply-weekly-cleaning-1", nil)
	if response.Code != http.StatusCreated || len(receipt.ChoreIDs) != 3 {
		t.Fatalf("apply status=%d body=%s", response.Code, response.Body)
	}
	var chores []chModel.Chore
	if err := h.db.Order("id").Find(&chores).Error; err != nil {
		t.Fatal(err)
	}
	if len(chores) != 3 || chores[0].CircleID != h.circle || chores[0].Points == nil || *chores[0].Points != points || !chores[0].RequireApproval {
		t.Fatalf("generated configuration mismatch: %#v", chores)
	}
	for _, generated := range chores {
		if generated.NextDueDate == nil || generated.NextDueDate.Before(time.Date(2030, 11, 20, 0, 0, 0, 0, time.UTC)) {
			t.Fatalf("stale due date generated for %q: %v", generated.Name, generated.NextDueDate)
		}
	}
	var ledgerCount int64
	h.db.Model(&pModel.PointsHistory{}).Count(&ledgerCount)
	if ledgerCount != 0 {
		t.Fatalf("apply changed points ledger: %d", ledgerCount)
	}
	response, replay := applyPack(t, h, pack.ID, "apply-weekly-cleaning-1", nil)
	if response.Code != http.StatusOK || len(replay.ChoreIDs) != 3 {
		t.Fatalf("replay status=%d body=%s", response.Code, response.Body)
	}
	h.db.Model(&chModel.Chore{}).Count(&ledgerCount)
	if ledgerCount != 3 {
		t.Fatalf("idempotent retry created duplicates: %d", ledgerCount)
	}

	if err := h.db.Model(&Item{}).Where("id = ?", pack.Items[0].ID).Updates(map[string]any{"name": "Updated bathrooms", "points": 9}).Error; err != nil {
		t.Fatal(err)
	}
	response, _ = applyPack(t, h, pack.ID, "apply-weekly-cleaning-2", nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("second apply status=%d body=%s", response.Code, response.Body)
	}
	var original chModel.Chore
	h.db.First(&original, receipt.ChoreIDs[0])
	if original.Name != "Bathrooms" || original.Points == nil || *original.Points != 5 {
		t.Fatalf("template edit mutated old chore: %#v", original)
	}
	var updated chModel.Chore
	h.db.Where("name = ?", "Updated bathrooms").First(&updated)
	if updated.Points == nil || *updated.Points != 9 {
		t.Fatalf("second apply did not use updated template: %#v", updated)
	}

	invalid := createPack(t, h, []Item{{Name: "Valid", FrequencyType: chModel.FrequencyTypeDaily, AssignStrategy: chModel.AssignmentStrategyNoAssignee}, {Name: "Invalid member", FrequencyType: chModel.FrequencyTypeDaily, AssignStrategy: chModel.AssignmentStrategyRandom, AssigneeIDs: []int{99999}}})
	var before int64
	h.db.Model(&chModel.Chore{}).Count(&before)
	response, _ = applyPack(t, h, invalid.ID, "atomic-invalid-pack-1", nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("invalid apply status=%d body=%s", response.Code, response.Body)
	}
	var after int64
	h.db.Model(&chModel.Chore{}).Count(&after)
	if after != before {
		t.Fatalf("partial application created chores: before=%d after=%d", before, after)
	}
}

func TestAssetMeterApplyUsesCurrentBaselineAndValidatesCircle(t *testing.T) {
	h := newRoutineHarness(t)
	now := time.Now().UTC()
	asset := tModel.Thing{CircleID: h.circle, UserID: h.userID, Name: "Toyota Camry", CreatedAt: &now, UpdatedAt: &now}
	if err := h.db.Create(&asset).Error; err != nil {
		t.Fatal(err)
	}
	meter := mModel.Meter{CircleID: h.circle, ThingID: &asset.ID, CreatedBy: h.userID, Name: "Odometer", Unit: mModel.UnitKilometers, CurrentReading: 93250, CreatedAt: now, UpdatedAt: now}
	if err := h.db.Create(&meter).Error; err != nil {
		t.Fatal(err)
	}
	interval := int64(10000)
	pack := createPack(t, h, []Item{{Name: "Oil change", FrequencyType: chModel.FrequencyTypeInterval, Frequency: 1, AssignStrategy: chModel.AssignmentStrategyNoAssignee, AssetID: &asset.ID, MeterID: &meter.ID, MeterInterval: &interval}})
	response, receipt := applyPack(t, h, pack.ID, "meter-current-baseline-1", nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("meter apply status=%d body=%s", response.Code, response.Body)
	}
	var schedule mModel.ChoreSchedule
	if err := h.db.Where("chore_id = ?", receipt.ChoreIDs[0]).First(&schedule).Error; err != nil {
		t.Fatal(err)
	}
	if schedule.Baseline != meter.CurrentReading || schedule.Interval != interval {
		t.Fatalf("runtime meter state was not freshly derived: %#v", schedule)
	}

	otherAsset := tModel.Thing{CircleID: 2, UserID: 99, Name: "Other", CreatedAt: &now, UpdatedAt: &now}
	if err := h.db.Create(&otherAsset).Error; err != nil {
		t.Fatal(err)
	}
	bad := createPack(t, h, []Item{{Name: "Unsafe asset", FrequencyType: chModel.FrequencyTypeDaily, AssignStrategy: chModel.AssignmentStrategyNoAssignee, AssetID: &otherAsset.ID}})
	response, _ = applyPack(t, h, bad.ID, "cross-circle-asset-1", nil)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("cross-circle asset status=%d body=%s", response.Code, response.Body)
	}
}

func TestSaveChoreAsTemplateStripsRuntimeStateAndApplies(t *testing.T) {
	h := newRoutineHarness(t)
	now := time.Now().UTC()
	due := now.Add(-90 * 24 * time.Hour)
	points := 7
	source := chModel.Chore{
		Name: "Clean kitchen", FrequencyType: chModel.FrequencyTypeWeekly,
		Frequency: 1, NextDueDate: &due, CircleID: h.circle, CreatedBy: h.userID,
		UpdatedBy: h.userID, CreatedAt: now, UpdatedAt: now, IsActive: false,
		AssignStrategy: chModel.AssignmentStrategyRoundRobin, Points: &points,
		RequireApproval: true, Status: chModel.ChoreStatusPendingApproval,
	}
	if err := h.db.Create(&source).Error; err != nil {
		t.Fatal(err)
	}
	if err := h.db.Create(&chModel.ChoreHistory{ChoreID: source.ID, CompletedBy: h.userID, PerformedAt: &now, Status: chModel.ChoreHistoryStatusCompleted}).Error; err != nil {
		t.Fatal(err)
	}
	pack := createPack(t, h, nil)
	ctx, response := h.context(http.MethodPost, "/", nil, gin.Params{{Key: "id", Value: strconv.Itoa(pack.ID)}, {Key: "choreId", Value: strconv.Itoa(source.ID)}})
	h.handler.SaveChore(ctx)
	if response.Code != http.StatusCreated {
		t.Fatalf("save-as-template status=%d body=%s", response.Code, response.Body)
	}
	var item Item
	if err := h.db.Where("pack_id = ?", pack.ID).First(&item).Error; err != nil {
		t.Fatal(err)
	}
	if item.Name != source.Name || item.Points == nil || *item.Points != points || !item.RequireApproval {
		t.Fatalf("reusable fields were not copied: %#v", item)
	}
	response, receipt := applyPack(t, h, pack.ID, "saved-chore-template-1", nil)
	if response.Code != http.StatusCreated {
		t.Fatalf("saved template apply status=%d body=%s", response.Code, response.Body)
	}
	var generated chModel.Chore
	if err := h.db.First(&generated, receipt.ChoreIDs[0]).Error; err != nil {
		t.Fatal(err)
	}
	if !generated.IsActive || generated.Status != chModel.ChoreStatusNoStatus || generated.NextDueDate == nil || generated.NextDueDate.Before(time.Date(2030, 11, 20, 0, 0, 0, 0, time.UTC)) {
		t.Fatalf("runtime state leaked into generated chore: %#v", generated)
	}
	var histories int64
	h.db.Model(&chModel.ChoreHistory{}).Where("chore_id = ?", generated.ID).Count(&histories)
	if histories != 0 {
		t.Fatalf("history leaked into generated chore: %d", histories)
	}

	if err := h.db.Model(&Pack{}).Where("id = ?", pack.ID).Update("is_active", false).Error; err != nil {
		t.Fatal(err)
	}
	response, _ = applyPack(t, h, pack.ID, "archived-template-1", nil)
	if response.Code != http.StatusConflict {
		t.Fatalf("archived routine apply status=%d body=%s", response.Code, response.Body)
	}
	if err := h.db.First(&generated, generated.ID).Error; err != nil {
		t.Fatalf("archiving template damaged generated chore: %v", err)
	}
}
