package shoppinglist

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	cModel "donetick.com/core/internal/circle/model"
	cRepo "donetick.com/core/internal/circle/repo"
	slModel "donetick.com/core/internal/shoppinglist/model"
	slRepo "donetick.com/core/internal/shoppinglist/repo"
	uModel "donetick.com/core/internal/user/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func shoppingTestContext(method, target string, body []byte, userID, circleID int) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, target, bytes.NewReader(body))
	if body != nil {
		ctx.Request.Header.Set("Content-Type", "application/json")
	}
	ctx.Set("id", &uModel.UserDetails{User: uModel.User{ID: userID, CircleID: circleID}})
	return ctx, recorder
}

func TestHandlerCreateListAndItem(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&slModel.ShoppingList{}, &slModel.ShoppingListItem{}, &cModel.UserCircle{}, &cModel.Circle{}); err != nil {
		t.Fatal(err)
	}
	db.Create(&cModel.Circle{ID: 3, Name: "Home"})
	db.Create(&cModel.UserCircle{UserID: 7, CircleID: 3, Role: cModel.UserRoleMember, IsActive: true})

	handler := NewHandler(slRepo.NewRepository(db), cRepo.NewCircleRepository(db))

	body, _ := json.Marshal(slModel.CreateListReq{Name: "Weekly shop", Scope: slModel.ScopePersonal})
	ctx, response := shoppingTestContext(http.MethodPost, "/api/v1/shopping-lists", body, 7, 3)
	handler.createList(ctx)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", response.Code, response.Body.String())
	}

	var created struct {
		Res slModel.ShoppingList `json:"res"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}

	itemBody, _ := json.Marshal(slModel.CreateItemReq{Name: "Bread"})
	ctx, response = shoppingTestContext(http.MethodPost, "/api/v1/shopping-lists/"+strconv.Itoa(created.Res.ID)+"/items", itemBody, 7, 3)
	ctx.Params = gin.Params{{Key: "id", Value: strconv.Itoa(created.Res.ID)}}
	handler.createItem(ctx)
	if response.Code != http.StatusCreated {
		t.Fatalf("create item status=%d body=%s", response.Code, response.Body.String())
	}
}
