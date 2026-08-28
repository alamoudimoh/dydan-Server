package maintenance

import (
	"bytes"
	"encoding/json"
	"testing"
	"time"

	chModel "github.com/alamoudimoh/daydan-server/internal/chore/model"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/daydan-server/internal/circle/repo"
	mModel "github.com/alamoudimoh/daydan-server/internal/meter/model"
	tModel "github.com/alamoudimoh/daydan-server/internal/thing/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// setupPrivacyTest creates a handler with two users (A and B) in the same circle.
// User A owns a Thing with a private chore and private chore history linked to it.
// User B is a regular member who must NOT see A's private chore data through
// maintenance endpoints.
func setupPrivacyTest(t *testing.T) (*Handler, *gorm.DB, uModel.User, uModel.User, tModel.Thing, chModel.Chore, chModel.Chore) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &cModel.UserCircle{}, &tModel.Thing{}, &tModel.ThingChore{}, &mModel.Meter{}, &mModel.Reading{}, &mModel.ChoreSchedule{}, &chModel.Chore{}, &chModel.ChoreHistory{}, &chModel.ChoreAssignees{}); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(db, cRepo.NewCircleRepository(db))
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	userA := uModel.User{ID: 10, Username: "alice", DisplayName: "Alice", CircleID: 5}
	userB := uModel.User{ID: 20, Username: "bob", DisplayName: "Bob", CircleID: 5}
	if err := db.Create(&[]uModel.User{userA, userB}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]cModel.UserCircle{
		{UserID: 10, CircleID: 5, Role: cModel.UserRoleAdmin, IsActive: true},
		{UserID: 20, CircleID: 5, Role: cModel.UserRoleMember, IsActive: true},
	}).Error; err != nil {
		t.Fatal(err)
	}

	// A creates a Thing (asset) in the shared circle
	asset := tModel.Thing{ID: 100, UserID: 10, CircleID: 5, IsShared: true, Name: "Alice's Car", Type: "text", CreatedAt: &now, UpdatedAt: &now}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatal(err)
	}

	// A creates a private chore linked to the Thing
	privateChore := chModel.Chore{ID: 200, Name: "Private oil change", CircleID: 5, CreatedBy: 10, IsActive: true, IsPrivate: true, NextDueDate: &now, CreatedAt: now, UpdatedAt: now}
	// A also creates a public chore linked to the Thing
	publicChore := chModel.Chore{ID: 201, Name: "Public tire rotation", CircleID: 5, CreatedBy: 10, IsActive: true, IsPrivate: false, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&[]chModel.Chore{privateChore, publicChore}).Error; err != nil {
		t.Fatal(err)
	}

	// Link both chores to the Thing
	if err := db.Create(&[]tModel.ThingChore{
		{ThingID: 100, ChoreID: 200},
		{ThingID: 100, ChoreID: 201},
	}).Error; err != nil {
		t.Fatal(err)
	}

	// Create private chore history
	performed := now
	if err := db.Create(&chModel.ChoreHistory{
		ChoreID: 200, PerformedAt: &performed, CompletedBy: 10,
		Status: chModel.ChoreHistoryStatusCompleted, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}
	// Create public chore history
	if err := db.Create(&chModel.ChoreHistory{
		ChoreID: 201, PerformedAt: &performed, CompletedBy: 10,
		Status: chModel.ChoreHistoryStatusCompleted, CreatedAt: now,
	}).Error; err != nil {
		t.Fatal(err)
	}

	return h, db, userA, userB, asset, privateChore, publicChore
}

// TestAUD06_PrivateChoreMaintenanceHiddenFromSameCircleMember verifies that
// user B (same circle, regular member) cannot see user A's private chore
// maintenance items or history through the Detail endpoint.
func TestAUD06_PrivateChoreMaintenanceHiddenFromSameCircleMember(t *testing.T) {
	h, db, userA, userB, asset, privateChore, publicChore := setupPrivacyTest(t)

	// User B requests Detail for the shared Thing
	bobCtx, resp := maintenanceContext("GET", "/maintenance/assets/100", uModel.UserDetails{User: userB}, nil)
	bobCtx.Params = gin.Params{{Key: "id", Value: "100"}}
	h.Detail(bobCtx)

	if resp.Code != 200 {
		t.Fatalf("user B Detail returned %d: %s", resp.Code, resp.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	res := result["res"].(map[string]interface{})

	// Maintenance items: B should only see the public chore, not the private one
	maintenance := res["maintenance"].([]interface{})
	if len(maintenance) != 1 {
		t.Fatalf("expected 1 maintenance item (public only), got %d", len(maintenance))
	}
	item := maintenance[0].(map[string]interface{})
	if int(item["choreId"].(float64)) != publicChore.ID {
		t.Fatalf("expected public chore %d in maintenance, got %v", publicChore.ID, item["choreId"])
	}

	// History: B should only see public chore history, not private
	history := res["history"].([]interface{})
	if len(history) != 1 {
		t.Fatalf("expected 1 history entry (public only), got %d", len(history))
	}
	hEntry := history[0].(map[string]interface{})
	if int(hEntry["choreId"].(float64)) != publicChore.ID {
		t.Fatalf("expected public chore history, got choreId %v", hEntry["choreId"])
	}

	// User A should see both private and public maintenance items
	aliceCtx, respA := maintenanceContext("GET", "/maintenance/assets/100", uModel.UserDetails{User: userA}, nil)
	aliceCtx.Params = gin.Params{{Key: "id", Value: "100"}}
	h.Detail(aliceCtx)

	if respA.Code != 200 {
		t.Fatalf("user A Detail returned %d: %s", respA.Code, respA.Body.String())
	}

	var resultA map[string]interface{}
	if err := json.Unmarshal(respA.Body.Bytes(), &resultA); err != nil {
		t.Fatal(err)
	}
	resA := resultA["res"].(map[string]interface{})
	maintenanceA := resA["maintenance"].([]interface{})
	if len(maintenanceA) != 2 {
		t.Fatalf("owner A should see 2 maintenance items, got %d", len(maintenanceA))
	}
	historyA := resA["history"].([]interface{})
	if len(historyA) != 2 {
		t.Fatalf("owner A should see 2 history entries, got %d", len(historyA))
	}

	_ = privateChore // used for clarity
	_ = db
	_ = asset
}

// TestAUD06_PrivateChoreMaintenanceHiddenInCenter verifies that the Center
// endpoint (maintenance overview) also filters private chore data.
func TestAUD06_PrivateChoreMaintenanceHiddenInCenter(t *testing.T) {
	h, _, userA, userB, _, privateChore, _ := setupPrivacyTest(t)

	// User B requests Center
	bobCtx, resp := maintenanceContext("GET", "/maintenance", uModel.UserDetails{User: userB}, nil)
	h.Center(bobCtx)

	if resp.Code != 200 {
		t.Fatalf("user B Center returned %d: %s", resp.Code, resp.Body.String())
	}

	// B must not see the private chore name in the response
	if bytes.Contains(resp.Body.Bytes(), []byte("Private oil change")) {
		t.Fatal("user B can see private chore name in Center response")
	}

	// User A should see it
	aliceCtx, respA := maintenanceContext("GET", "/maintenance", uModel.UserDetails{User: userA}, nil)
	h.Center(aliceCtx)

	if respA.Code != 200 {
		t.Fatalf("user A Center returned %d: %s", respA.Code, respA.Body.String())
	}
	if !bytes.Contains(respA.Body.Bytes(), []byte("Private oil change")) {
		t.Fatal("owner A cannot see private chore name in Center response")
	}

	_ = privateChore
}

// TestAUD06_AdminDoesNotBypassPrivateChorePrivacy verifies that admin/manager
// status does not automatically bypass personal-data boundaries for private
// chore maintenance/history (unless the admin is the creator or assignee).
func TestAUD06_AdminDoesNotBypassPrivateChorePrivacy(t *testing.T) {
	h, db, userA, _, asset, privateChore, _ := setupPrivacyTest(t)

	// Create user C as admin in the same circle
	userC := uModel.User{ID: 30, Username: "carol", DisplayName: "Carol", CircleID: 5}
	if err := db.Create(&userC).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.UserCircle{UserID: 30, CircleID: 5, Role: cModel.UserRoleAdmin, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}

	// User C (admin, not creator/assignee of the private chore) requests Detail
	carolCtx, resp := maintenanceContext("GET", "/maintenance/assets/100", uModel.UserDetails{User: userC}, nil)
	carolCtx.Params = gin.Params{{Key: "id", Value: "100"}}
	h.Detail(carolCtx)

	if resp.Code != 200 {
		t.Fatalf("admin C Detail returned %d: %s", resp.Code, resp.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	res := result["res"].(map[string]interface{})

	// Admin C should NOT see the private chore maintenance item
	maintenance := res["maintenance"].([]interface{})
	for _, m := range maintenance {
		item := m.(map[string]interface{})
		if int(item["choreId"].(float64)) == privateChore.ID {
			t.Fatal("admin C (not creator/assignee) can see private chore maintenance item")
		}
	}

	// Admin C should NOT see private chore history
	history := res["history"].([]interface{})
	for _, hEntry := range history {
		entry := hEntry.(map[string]interface{})
		if int(entry["choreId"].(float64)) == privateChore.ID {
			t.Fatal("admin C (not creator/assignee) can see private chore history")
		}
	}

	_ = userA
	_ = asset
}

func TestAUD06_UnrelatedManagerCannotLinkPrivateChore(t *testing.T) {
	h, db, _, _, _, privateChore, _ := setupPrivacyTest(t)
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	manager := uModel.User{ID: 30, Username: "carol", DisplayName: "Carol", CircleID: 5}
	sharedAsset := tModel.Thing{ID: 102, UserID: manager.ID, CircleID: 5, IsShared: true, Name: "Shared boiler", Type: "text", CreatedAt: &now, UpdatedAt: &now}
	if err := db.Create(&manager).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.UserCircle{UserID: manager.ID, CircleID: 5, Role: cModel.UserRoleManager, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&sharedAsset).Error; err != nil {
		t.Fatal(err)
	}

	ctx, response := maintenanceContext("POST", "/maintenance/assets/102/links", uModel.UserDetails{User: manager}, linkRequest{ChoreID: &privateChore.ID})
	ctx.Params = gin.Params{{Key: "id", Value: "102"}}
	h.Link(ctx)
	if response.Code != 404 {
		t.Fatalf("unrelated manager linked a private chore: %d %s", response.Code, response.Body.String())
	}
	var link tModel.ThingChore
	if err := db.Where("chore_id = ?", privateChore.ID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.ThingID == sharedAsset.ID {
		t.Fatal("private chore relationship was reassigned by unrelated manager")
	}
}

// TestAUD06_AssigneeCanSeePrivateChoreMaintenance verifies that a user assigned
// to a private chore can see its maintenance data through the Thing detail.
func TestAUD06_AssigneeCanSeePrivateChoreMaintenance(t *testing.T) {
	h, db, userA, userB, asset, privateChore, _ := setupPrivacyTest(t)

	// The asset itself is personal. B can still see it because B is explicitly
	// assigned to its private chore.
	if err := db.Model(&tModel.Thing{}).Where("id = ?", asset.ID).Update("is_shared", false).Error; err != nil {
		t.Fatal(err)
	}

	// Assign user B as an assignee of the private chore
	if err := db.Create(&chModel.ChoreAssignees{ChoreID: privateChore.ID, UserID: userB.ID}).Error; err != nil {
		t.Fatal(err)
	}

	// Now user B should see the private chore maintenance
	bobCtx, resp := maintenanceContext("GET", "/maintenance/assets/100", uModel.UserDetails{User: userB}, nil)
	bobCtx.Params = gin.Params{{Key: "id", Value: "100"}}
	h.Detail(bobCtx)

	if resp.Code != 200 {
		t.Fatalf("assignee B Detail returned %d: %s", resp.Code, resp.Body.String())
	}

	var result map[string]interface{}
	if err := json.Unmarshal(resp.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	res := result["res"].(map[string]interface{})
	maintenance := res["maintenance"].([]interface{})
	foundPrivate := false
	for _, m := range maintenance {
		item := m.(map[string]interface{})
		if int(item["choreId"].(float64)) == privateChore.ID {
			foundPrivate = true
		}
	}
	if !foundPrivate {
		t.Fatal("assignee B cannot see private chore maintenance item")
	}

	_ = userA
	_ = asset
}

// TestAUD06_PersonalAssetNeverLeaksToPeers verifies that ordinary Things keep
// the owner-only policy used by /things when viewed through maintenance APIs.
func TestAUD06_PersonalAssetNeverLeaksToPeers(t *testing.T) {
	h, db, userA, userB, _, _, _ := setupPrivacyTest(t)
	now := time.Date(2026, 8, 1, 11, 0, 0, 0, time.UTC)
	personal := tModel.Thing{ID: 101, UserID: userA.ID, CircleID: 5, Name: "Alice private medical device", Type: "text", CreatedAt: &now, UpdatedAt: &now}
	privateChore := chModel.Chore{ID: 202, Name: "Private calibration", CircleID: 5, CreatedBy: userA.ID, IsActive: true, IsPrivate: true, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&personal).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&privateChore).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&tModel.ThingChore{ThingID: personal.ID, ChoreID: privateChore.ID}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&chModel.ChoreHistory{ChoreID: privateChore.ID, PerformedAt: &now, CompletedBy: userA.ID, Status: chModel.ChoreHistoryStatusCompleted, CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}

	// Same-circle peer cannot fetch the resource at all.
	bobCtx, detail := maintenanceContext("GET", "/maintenance/assets/101", uModel.UserDetails{User: userB}, nil)
	bobCtx.Params = gin.Params{{Key: "id", Value: "101"}}
	h.Detail(bobCtx)
	if detail.Code != 404 {
		t.Fatalf("personal asset detail leaked to peer: %d %s", detail.Code, detail.Body.String())
	}

	// The database-backed list and center queries also exclude it, rather than
	// relying on response filtering after the private row is loaded.
	bobCtx, list := maintenanceContext("GET", "/maintenance/assets", uModel.UserDetails{User: userB}, nil)
	h.List(bobCtx)
	if list.Code != 200 || bytes.Contains(list.Body.Bytes(), []byte(personal.Name)) {
		t.Fatalf("personal asset leaked from list: %d %s", list.Code, list.Body.String())
	}
	bobCtx, center := maintenanceContext("GET", "/maintenance", uModel.UserDetails{User: userB}, nil)
	h.Center(bobCtx)
	if center.Code != 200 || bytes.Contains(center.Body.Bytes(), []byte(personal.Name)) || bytes.Contains(center.Body.Bytes(), []byte(privateChore.Name)) {
		t.Fatalf("personal asset or history leaked from center: %d %s", center.Code, center.Body.String())
	}

	// A different adult admin has no personal-data bypass.
	admin := uModel.User{ID: 30, Username: "carol", DisplayName: "Carol", CircleID: 5}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.UserCircle{UserID: admin.ID, CircleID: 5, Role: cModel.UserRoleAdmin, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	adminCtx, adminDetail := maintenanceContext("GET", "/maintenance/assets/101", uModel.UserDetails{User: admin}, nil)
	adminCtx.Params = gin.Params{{Key: "id", Value: "101"}}
	h.Detail(adminCtx)
	if adminDetail.Code != 404 {
		t.Fatalf("non-owner admin bypassed personal asset boundary: %d %s", adminDetail.Code, adminDetail.Body.String())
	}

	// The owner keeps complete access.
	ownerCtx, ownerDetail := maintenanceContext("GET", "/maintenance/assets/101", uModel.UserDetails{User: userA}, nil)
	ownerCtx.Params = gin.Params{{Key: "id", Value: "101"}}
	h.Detail(ownerCtx)
	if ownerDetail.Code != 200 || !bytes.Contains(ownerDetail.Body.Bytes(), []byte(privateChore.Name)) {
		t.Fatalf("owner lost personal maintenance access: %d %s", ownerDetail.Code, ownerDetail.Body.String())
	}
}

// TestAUD07_ManagedChildCannotManageAssets verifies that a managed child
// account with a stale admin role is capped to member and cannot use
// canManage-gated endpoints (Create, Update, Archive, Link).
func TestAUD07_ManagedChildCannotManageAssets(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &cModel.UserCircle{}, &tModel.Thing{}, &tModel.ThingChore{}, &mModel.Meter{}, &mModel.Reading{}, &mModel.ChoreSchedule{}, &chModel.Chore{}, &chModel.ChoreHistory{}); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(db, cRepo.NewCircleRepository(db))
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	// Parent admin
	parent := uModel.User{ID: 1, Username: "parent", DisplayName: "Parent", CircleID: 1, UserType: uModel.UserTypeParent}
	// Managed child with STALE admin role
	child := uModel.User{ID: 2, Username: "parent_kid", DisplayName: "Kid", CircleID: 1, UserType: uModel.UserTypeChild, ParentUserID: func() *int { v := 1; return &v }()}
	if err := db.Create(&[]uModel.User{parent, child}).Error; err != nil {
		t.Fatal(err)
	}
	// Child has stale admin membership
	if err := db.Create(&[]cModel.UserCircle{
		{UserID: 1, CircleID: 1, Role: cModel.UserRoleAdmin, IsActive: true},
		{UserID: 2, CircleID: 1, Role: cModel.UserRoleAdmin, IsActive: true},
	}).Error; err != nil {
		t.Fatal(err)
	}

	asset := tModel.Thing{ID: 50, UserID: 1, CircleID: 1, Name: "Family Car", Type: "text", CreatedAt: &now, UpdatedAt: &now}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatal(err)
	}

	// Child attempts Create (canManage should fail)
	childUser := uModel.UserDetails{User: child}
	c, resp := maintenanceContext("POST", "/maintenance/assets", childUser, assetRequest{Name: "New Asset"})
	h.Create(c)
	if resp.Code != 403 {
		t.Fatalf("managed child with stale admin role should be denied Create, got %d", resp.Code)
	}

	// Child attempts Update
	c, resp = maintenanceContext("PUT", "/maintenance/assets/50", childUser, assetRequest{Name: "Renamed"})
	c.Params = gin.Params{{Key: "id", Value: "50"}}
	h.Update(c)
	if resp.Code != 403 {
		t.Fatalf("managed child with stale admin role should be denied Update, got %d", resp.Code)
	}

	// Child attempts Archive
	c, resp = maintenanceContext("DELETE", "/maintenance/assets/50", childUser, nil)
	c.Params = gin.Params{{Key: "id", Value: "50"}}
	h.Archive(c)
	if resp.Code != 403 {
		t.Fatalf("managed child with stale admin role should be denied Archive, got %d", resp.Code)
	}

	// Child attempts Link
	c, resp = maintenanceContext("POST", "/maintenance/assets/50/links", childUser, linkRequest{ChoreID: func() *int { v := 1; return &v }()})
	c.Params = gin.Params{{Key: "id", Value: "50"}}
	h.Link(c)
	if resp.Code != 403 {
		t.Fatalf("managed child with stale admin role should be denied Link, got %d", resp.Code)
	}

	// Parent admin should still be allowed
	parentUser := uModel.UserDetails{User: parent}
	c, resp = maintenanceContext("POST", "/maintenance/assets", parentUser, assetRequest{Name: "New Asset"})
	h.Create(c)
	if resp.Code != 201 {
		t.Fatalf("parent admin should be allowed Create, got %d: %s", resp.Code, resp.Body.String())
	}
	var created tModel.Thing
	if err := db.Where("name = ?", "New Asset").First(&created).Error; err != nil || !created.IsShared {
		t.Fatalf("maintenance-created asset must be explicitly shared: asset=%#v err=%v", created, err)
	}
}

// TestAUD07_ManagedChildWithManagerRoleCannotManage verifies that a managed
// child with a stale manager role is also capped.
func TestAUD07_ManagedChildWithManagerRoleCannotManage(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &cModel.UserCircle{}, &tModel.Thing{}, &tModel.ThingChore{}, &mModel.Meter{}, &mModel.Reading{}, &mModel.ChoreSchedule{}, &chModel.Chore{}, &chModel.ChoreHistory{}); err != nil {
		t.Fatal(err)
	}
	h := NewHandler(db, cRepo.NewCircleRepository(db))

	child := uModel.User{ID: 2, Username: "parent_kid", DisplayName: "Kid", CircleID: 1, UserType: uModel.UserTypeChild, ParentUserID: func() *int { v := 1; return &v }()}
	parent := uModel.User{ID: 1, Username: "parent", DisplayName: "Parent", CircleID: 1, UserType: uModel.UserTypeParent}
	if err := db.Create(&[]uModel.User{parent, child}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]cModel.UserCircle{
		{UserID: 1, CircleID: 1, Role: cModel.UserRoleAdmin, IsActive: true},
		{UserID: 2, CircleID: 1, Role: cModel.UserRoleManager, IsActive: true},
	}).Error; err != nil {
		t.Fatal(err)
	}

	childUser := uModel.UserDetails{User: child}
	c, resp := maintenanceContext("POST", "/maintenance/assets", childUser, assetRequest{Name: "New Asset"})
	h.Create(c)
	if resp.Code != 403 {
		t.Fatalf("managed child with stale manager role should be denied Create, got %d", resp.Code)
	}
}
