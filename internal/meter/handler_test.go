package meter

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	mModel "github.com/alamoudimoh/daydan-server/internal/meter/model"
	mRepo "github.com/alamoudimoh/daydan-server/internal/meter/repo"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func meterHandlerTestContext(method, target string, body []byte, userID, circleID int) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, target, bytes.NewReader(body))
	ctx.Request.Header.Set("Content-Type", "application/json")
	ctx.Set("id", &uModel.UserDetails{User: uModel.User{ID: userID, CircleID: circleID}})
	return ctx, recorder
}

func TestMeterAPIRejectsCrossCircleReadUpdateAndHistory(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&mModel.Meter{}, &mModel.Reading{}, &mModel.ChoreSchedule{}); err != nil {
		t.Fatal(err)
	}
	repository := mRepo.NewRepository(db)
	handler := NewHandler(repository, nil)
	now := time.Now().UTC()
	meter := &mModel.Meter{CircleID: 1, CreatedBy: 1, Name: "Circle 1 Odometer", Unit: mModel.UnitKilometers, CurrentReading: 80000, CreatedAt: now, UpdatedAt: now}
	if err := repository.Create(t.Context(), meter, 1); err != nil {
		t.Fatal(err)
	}
	id := strconv.Itoa(meter.ID)

	ctx, response := meterHandlerTestContext(http.MethodPost, "/api/v1/meters/"+id+"/readings", []byte(`{"reading":90000}`), 2, 2)
	ctx.Params = gin.Params{{Key: "id", Value: id}}
	handler.AddReading(ctx)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-circle update status=%d body=%s", response.Code, response.Body)
	}

	ctx, response = meterHandlerTestContext(http.MethodGet, "/api/v1/meters/"+id+"/readings", nil, 2, 2)
	ctx.Params = gin.Params{{Key: "id", Value: id}}
	handler.History(ctx)
	if response.Code != http.StatusNotFound {
		t.Fatalf("cross-circle history status=%d body=%s", response.Code, response.Body)
	}

	ctx, response = meterHandlerTestContext(http.MethodGet, "/api/v1/meters", nil, 2, 2)
	handler.List(ctx)
	if response.Code != http.StatusOK {
		t.Fatalf("cross-circle list status=%d body=%s", response.Code, response.Body)
	}
	var payload struct {
		Res []mModel.Meter `json:"res"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Res) != 0 {
		t.Fatalf("cross-circle list leaked meters: %#v", payload.Res)
	}
	current, err := repository.Get(t.Context(), meter.ID, 1)
	if err != nil || current.CurrentReading != 80000 {
		t.Fatalf("cross-circle API attempt changed meter: meter=%#v err=%v", current, err)
	}
}
