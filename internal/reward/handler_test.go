package reward

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	cRepo "donetick.com/core/internal/circle/repo"
	uModel "donetick.com/core/internal/user/model"
	"github.com/gin-gonic/gin"
)

func rewardContext(method, path, body string, userID, circleID int) (*gin.Context, *httptest.ResponseRecorder) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(method, path, bytes.NewBufferString(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", &uModel.UserDetails{User: uModel.User{ID: userID, CircleID: circleID}})
	return c, w
}
func TestHandlerAuthorizationAndRequesterIdentity(t *testing.T) {
	repo, db := testRepo(t)
	seed(t, db)
	h := NewHandler(repo, cRepo.NewCircleRepository(db))
	rw := Reward{CircleID: 10, Name: "Dinner", CostPoints: 100, IsActive: true, CreatedBy: 1}
	repo.Create(t.Context(), &rw)
	c, w := rewardContext(http.MethodPost, "/rewards", `{"name":"Forbidden","costPoints":1}`, 2, 10)
	h.create(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member create status=%d body=%s", w.Code, w.Body.String())
	}
	c, w = rewardContext(http.MethodPost, "/rewards/requests", `{"rewardId":`+strconv.Itoa(rw.ID)+`,"userId":3}`, 2, 10)
	h.request(c)
	if w.Code != http.StatusCreated {
		t.Fatalf("request status=%d body=%s", w.Code, w.Body.String())
	}
	var req RewardRequest
	if err := db.First(&req).Error; err != nil {
		t.Fatal(err)
	}
	if req.UserID != 2 {
		t.Fatalf("spoofed requester stored as %d", req.UserID)
	}
	c, w = rewardContext(http.MethodPut, "/rewards/"+strconv.Itoa(rw.ID), `{"name":"Cross circle","costPoints":1}`, 1, 20)
	c.Params = gin.Params{{Key: "id", Value: strconv.Itoa(rw.ID)}}
	h.update(c)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cross circle update status=%d body=%s", w.Code, w.Body.String())
	}
	c, w = rewardContext(http.MethodPost, "/requests/1/approve", `{}`, 2, 10)
	c.Params = gin.Params{{Key: "id", Value: "1"}}
	h.resolve(c, RequestStatusApproved)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member approval status=%d", w.Code)
	}
}
