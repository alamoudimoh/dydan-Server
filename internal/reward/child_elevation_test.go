package reward

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"

	cRepo "github.com/alamoudimoh/daydan-server/internal/circle/repo"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
)

// A managed child account must never exercise elevated Circle capabilities,
// even if a stale or inconsistent membership row grants it an admin role.
func TestChildAccountCannotExerciseRewardAdmin(t *testing.T) {
	repo, db := testRepo(t)
	seed(t, db)
	h := NewHandler(repo, cRepo.NewCircleRepository(db), nil)

	// Give the member (user 2) a child user type while keeping its admin row.
	var member uModel.User
	if err := db.First(&member, 2).Error; err != nil {
		t.Fatal(err)
	}
	member.UserType = uModel.UserTypeChild
	if err := db.Save(&member).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&cModel.UserCircle{}).Where("user_id = ? AND circle_id = ?", 2, 10).Update("role", cModel.UserRoleAdmin).Error; err != nil {
		t.Fatal(err)
	}

	// The authenticated principal is hydrated from the DB at request time,
	// so its user type is authoritative: child.
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/rewards", bytes.NewBufferString(`{"name":"Child reward","costPoints":1}`))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Set("id", &uModel.UserDetails{User: uModel.User{ID: 2, CircleID: 10, UserType: uModel.UserTypeChild}})
	h.create(c)
	if w.Code != http.StatusForbidden {
		t.Fatalf("child with stale admin row must not create rewards: status=%d body=%s", w.Code, w.Body.String())
	}
}
