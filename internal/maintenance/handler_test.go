package maintenance

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/dydan-server/internal/circle/repo"
	mModel "github.com/alamoudimoh/dydan-server/internal/meter/model"
	tModel "github.com/alamoudimoh/dydan-server/internal/thing/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func maintenanceTestHandler(t *testing.T) (*Handler, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &cModel.UserCircle{}, &tModel.Thing{}, &tModel.ThingChore{}, &mModel.Meter{}, &mModel.Reading{}, &mModel.ChoreSchedule{}, &chModel.Chore{}, &chModel.ChoreHistory{}, &chModel.ChoreAssignees{}); err != nil {
		t.Fatal(err)
	}
	return NewHandler(db, cRepo.NewCircleRepository(db)), db
}

func maintenanceContext(method, path string, user uModel.UserDetails, body interface{}) (*gin.Context, *httptest.ResponseRecorder) {
	var encoded []byte
	if body != nil {
		encoded, _ = json.Marshal(body)
	}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(method, path, bytes.NewReader(encoded))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", &user)
	return c, recorder
}

func TestAssetCircleIsolationLinkingArchiveAndHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db := maintenanceTestHandler(t)
	now := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	users := []uModel.User{{ID: 1, Username: "admin", DisplayName: "Mohammed", CircleID: 1}, {ID: 2, Username: "other", CircleID: 2}}
	if err := db.Create(&users).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]cModel.UserCircle{{UserID: 1, CircleID: 1, Role: cModel.UserRoleAdmin, IsActive: true}, {UserID: 2, CircleID: 2, Role: cModel.UserRoleAdmin, IsActive: true}}).Error; err != nil {
		t.Fatal(err)
	}
	asset := tModel.Thing{UserID: 1, CircleID: 1, Name: "Toyota Camry", Type: "text", CreatedAt: &now, UpdatedAt: &now}
	otherAsset := tModel.Thing{UserID: 2, CircleID: 2, Name: "Other circle", Type: "text", CreatedAt: &now, UpdatedAt: &now}
	if err := db.Create(&asset).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&otherAsset).Error; err != nil {
		t.Fatal(err)
	}
	meter := mModel.Meter{CircleID: 1, CreatedBy: 1, Name: "Odometer", Unit: mModel.UnitKilometers, CurrentReading: 90001, CreatedAt: now, UpdatedAt: now}
	otherMeter := mModel.Meter{CircleID: 2, CreatedBy: 2, Name: "Foreign", Unit: mModel.UnitHours, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&meter).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&otherMeter).Error; err != nil {
		t.Fatal(err)
	}
	due := now.AddDate(0, 3, 0)
	chores := []chModel.Chore{{Name: "Oil change", CircleID: 1, CreatedBy: 1, IsActive: true, NextDueDate: &due, CreatedAt: now, UpdatedAt: now}, {Name: "Tire rotation", CircleID: 1, CreatedBy: 1, IsActive: true, CreatedAt: now, UpdatedAt: now}, {Name: "Foreign chore", CircleID: 2, CreatedBy: 2, IsActive: true, CreatedAt: now, UpdatedAt: now}}
	if err := db.Create(&chores).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]tModel.ThingChore{{ThingID: asset.ID, ChoreID: chores[0].ID}, {ThingID: asset.ID, ChoreID: chores[1].ID}}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]mModel.ChoreSchedule{{ChoreID: chores[0].ID, MeterID: meter.ID, Interval: 10000, Baseline: 80000, CalendarDueDate: &due}, {ChoreID: chores[1].ID, MeterID: meter.ID, Interval: 8000, Baseline: 85000}}).Error; err != nil {
		t.Fatal(err)
	}
	performed := now
	if err := db.Create(&chModel.ChoreHistory{ChoreID: chores[0].ID, PerformedAt: &performed, CompletedBy: 1, Status: chModel.ChoreHistoryStatusCompleted, MeterID: &meter.ID, MeterReading: func() *int64 { v := int64(90000); return &v }(), CreatedAt: now}).Error; err != nil {
		t.Fatal(err)
	}

	detail, err := h.buildDetail(&gin.Context{}, asset, 1, 1)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Status != "attention" || len(detail.Maintenance) != 2 || len(detail.History) != 1 {
		t.Fatalf("unexpected detail: %#v", detail)
	}
	if detail.Maintenance[0].DueReason != "meter" || detail.Maintenance[0].MeterSchedule.Target != 90000 {
		t.Fatalf("meter state not authoritative: %#v", detail.Maintenance[0])
	}
	if detail.Maintenance[1].MeterSchedule.Baseline != 85000 {
		t.Fatal("shared meter schedules lost independent baselines")
	}
	c, response := maintenanceContext("GET", "/maintenance", uModel.UserDetails{User: users[0]}, nil)
	h.Center(c)
	if response.Code != 200 || !bytes.Contains(response.Body.Bytes(), []byte(`"due"`)) {
		t.Fatalf("maintenance center returned %d: %s", response.Code, response.Body.String())
	}

	admin := uModel.UserDetails{User: users[0]}
	c, response = maintenanceContext("GET", "/assets/"+itoa(otherAsset.ID), admin, nil)
	c.Params = gin.Params{{Key: "id", Value: itoa(otherAsset.ID)}}
	h.Detail(c)
	if response.Code != 404 {
		t.Fatalf("cross-circle read returned %d", response.Code)
	}
	c, response = maintenanceContext("POST", "/assets/"+itoa(asset.ID)+"/links", admin, linkRequest{MeterID: &otherMeter.ID})
	c.Params = gin.Params{{Key: "id", Value: itoa(asset.ID)}}
	h.Link(c)
	if response.Code != 404 {
		t.Fatalf("cross-circle meter link returned %d", response.Code)
	}
	c, response = maintenanceContext("POST", "/assets/"+itoa(asset.ID)+"/links", admin, linkRequest{ChoreID: &chores[2].ID})
	c.Params = gin.Params{{Key: "id", Value: itoa(asset.ID)}}
	h.Link(c)
	if response.Code != 404 {
		t.Fatalf("cross-circle chore link returned %d", response.Code)
	}
	c, response = maintenanceContext("DELETE", "/assets/"+itoa(asset.ID), admin, nil)
	c.Params = gin.Params{{Key: "id", Value: itoa(asset.ID)}}
	h.Archive(c)
	if response.Code != 204 {
		t.Fatalf("archive returned %d: %s", response.Code, response.Body.String())
	}
	c, response = maintenanceContext("PATCH", "/assets/"+itoa(asset.ID)+"/archive", admin, map[string]bool{"archived": false})
	c.Params = gin.Params{{Key: "id", Value: itoa(asset.ID)}}
	h.SetArchiveState(c)
	if response.Code != 204 {
		t.Fatalf("unarchive returned %d: %s", response.Code, response.Body.String())
	}
	c, response = maintenanceContext("DELETE", "/assets/"+itoa(asset.ID)+"/links?choreId="+itoa(chores[1].ID), admin, nil)
	c.Params = gin.Params{{Key: "id", Value: itoa(asset.ID)}}
	h.Unlink(c)
	if response.Code != 204 {
		t.Fatalf("unlink chore returned %d: %s", response.Code, response.Body.String())
	}
	var links int64
	db.Model(&tModel.ThingChore{}).Where("thing_id = ? AND chore_id = ?", asset.ID, chores[1].ID).Count(&links)
	if links != 0 {
		t.Fatal("unlink did not persist")
	}
	var count int64
	db.Model(&chModel.ChoreHistory{}).Where("chore_id = ?", chores[0].ID).Count(&count)
	if count != 1 {
		t.Fatal("archive removed maintenance history")
	}
}

func itoa(value int) string { return strconv.Itoa(value) }
