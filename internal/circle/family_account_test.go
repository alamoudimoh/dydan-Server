package circle

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/dydan-server/internal/circle/repo"
	"github.com/alamoudimoh/dydan-server/internal/storage"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func familyAccountContext(method, target string, user uModel.User) (*gin.Context, *httptest.ResponseRecorder) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(method, target, nil)
	ctx.Set("id", &uModel.UserDetails{User: user})
	return ctx, recorder
}

func TestIndependentFamilyAccountInviteApprovalFlow(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserNotificationTarget{}, &cModel.Circle{}, &cModel.UserCircle{}); err != nil {
		t.Fatal(err)
	}

	now := time.Date(2026, time.August, 10, 12, 0, 0, 0, time.UTC)
	mohammedCircle := cModel.Circle{Name: "Mohammed family", InviteCode: "family-invite", CreatedAt: now, UpdatedAt: now}
	abdulmajeedCircle := cModel.Circle{Name: "Abdulmajeed's circle", InviteCode: "personal-invite", CreatedAt: now, UpdatedAt: now}
	if err := db.Create(&mohammedCircle).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&abdulmajeedCircle).Error; err != nil {
		t.Fatal(err)
	}
	mohammed := uModel.User{Username: "mohammed", Email: "mohammed@example.test", DisplayName: "Mohammed", CircleID: mohammedCircle.ID, UserType: uModel.UserTypeParent, CreatedAt: now, UpdatedAt: now}
	abdulmajeed := uModel.User{Username: "abdulmajeed", Email: "abdulmajeed@example.test", DisplayName: "Abdulmajeed", CircleID: abdulmajeedCircle.ID, UserType: uModel.UserTypeParent, CreatedAt: now, UpdatedAt: now}
	nonAdmin := uModel.User{Username: "familymember", Email: "member@example.test", DisplayName: "Family Member", CircleID: mohammedCircle.ID, UserType: uModel.UserTypeParent, CreatedAt: now, UpdatedAt: now}
	managed := uModel.User{Username: "mohammed_child", DisplayName: "Child", CircleID: mohammedCircle.ID, ParentUserID: &mohammed.ID, UserType: uModel.UserTypeChild, CreatedAt: now, UpdatedAt: now}
	for _, user := range []*uModel.User{&mohammed, &abdulmajeed, &nonAdmin} {
		if err := db.Create(user).Error; err != nil {
			t.Fatal(err)
		}
	}
	managed.ParentUserID = &mohammed.ID
	if err := db.Create(&managed).Error; err != nil {
		t.Fatal(err)
	}
	for _, membership := range []*cModel.UserCircle{
		{UserID: mohammed.ID, CircleID: mohammedCircle.ID, Role: cModel.UserRoleAdmin, IsActive: true, CreatedAt: now, UpdatedAt: now},
		{UserID: abdulmajeed.ID, CircleID: abdulmajeedCircle.ID, Role: cModel.UserRoleAdmin, IsActive: true, CreatedAt: now, UpdatedAt: now},
		{UserID: nonAdmin.ID, CircleID: mohammedCircle.ID, Role: cModel.UserRoleMember, IsActive: true, CreatedAt: now, UpdatedAt: now},
		{UserID: managed.ID, CircleID: mohammedCircle.ID, Role: cModel.UserRoleMember, IsActive: true, CreatedAt: now, UpdatedAt: now},
	} {
		if err := db.Create(membership).Error; err != nil {
			t.Fatal(err)
		}
	}

	cfg := &config.Config{Jwt: config.JwtConfig{Secret: "test-secret"}}
	handler := NewHandler(cRepo.NewCircleRepository(db), uRepo.NewUserRepository(db, cfg), nil, nil, storage.NewURLSignerLocal(cfg), cfg)

	ctx, response := familyAccountContext(http.MethodPost, "/api/v1/circles/join?invite_code=family-invite", abdulmajeed)
	ctx.Request.URL.RawQuery = "invite_code=family-invite"
	handler.JoinCircle(ctx)
	if response.Code != http.StatusOK {
		t.Fatalf("join status=%d body=%s", response.Code, response.Body)
	}

	var request cModel.UserCircle
	if err := db.Where("user_id = ? AND circle_id = ?", abdulmajeed.ID, mohammedCircle.ID).First(&request).Error; err != nil {
		t.Fatal(err)
	}
	if request.IsActive {
		t.Fatal("join request became active before approval")
	}
	var savedUser uModel.User
	if err := db.First(&savedUser, abdulmajeed.ID).Error; err != nil {
		t.Fatal(err)
	}
	if savedUser.Username != "abdulmajeed" || savedUser.CircleID != abdulmajeedCircle.ID || savedUser.ParentUserID != nil || savedUser.UserType != uModel.UserTypeParent {
		t.Fatalf("independent account changed before approval: %#v", savedUser)
	}

	ctx, response = familyAccountContext(http.MethodGet, "/api/v1/circles/members", abdulmajeed)
	handler.GetCircleMembers(ctx)
	if response.Code != http.StatusOK {
		t.Fatalf("pre-approval members status=%d body=%s", response.Code, response.Body)
	}
	var membersPayload struct {
		Res []cModel.UserCircleDetail `json:"res"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &membersPayload); err != nil {
		t.Fatal(err)
	}
	for _, member := range membersPayload.Res {
		if member.UserID == mohammed.ID {
			t.Fatal("pending user accessed invited circle members before approval")
		}
	}

	ctx, response = familyAccountContext(http.MethodGet, "/api/v1/circles/members/requests", mohammed)
	handler.GetPendingCircleMembers(ctx)
	if response.Code != http.StatusOK {
		t.Fatalf("pending requests status=%d body=%s", response.Code, response.Body)
	}
	var pendingPayload struct {
		Res []cModel.UserCircleDetail `json:"res"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &pendingPayload); err != nil {
		t.Fatal(err)
	}
	if len(pendingPayload.Res) != 1 || pendingPayload.Res[0].Username != "abdulmajeed" {
		t.Fatalf("admin did not see Abdulmajeed's request: %#v", pendingPayload.Res)
	}

	acceptTarget := "/api/v1/circles/members/requests/accept?requestId=" + strconv.Itoa(request.ID)
	ctx, response = familyAccountContext(http.MethodPut, acceptTarget, nonAdmin)
	ctx.Request.URL.RawQuery = "requestId=" + strconv.Itoa(request.ID)
	handler.AcceptJoinRequest(ctx)
	if response.Code != http.StatusForbidden {
		t.Fatalf("non-admin acceptance status=%d body=%s", response.Code, response.Body)
	}

	ctx, response = familyAccountContext(http.MethodPut, acceptTarget, mohammed)
	ctx.Request.URL.RawQuery = "requestId=" + strconv.Itoa(request.ID)
	handler.AcceptJoinRequest(ctx)
	if response.Code != http.StatusOK {
		t.Fatalf("admin acceptance status=%d body=%s", response.Code, response.Body)
	}
	if err := db.First(&request, request.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !request.IsActive {
		t.Fatal("approved membership remained inactive")
	}
	if err := db.First(&savedUser, abdulmajeed.ID).Error; err != nil {
		t.Fatal(err)
	}
	if savedUser.CircleID != mohammedCircle.ID || savedUser.Username != "abdulmajeed" {
		t.Fatalf("approved independent account is wrong: %#v", savedUser)
	}
	if managed.Username != "mohammed_child" || managed.ParentUserID == nil || *managed.ParentUserID != mohammed.ID || managed.UserType != uModel.UserTypeChild {
		t.Fatalf("managed-account model changed: %#v", managed)
	}
}

func TestJoinCircleRejectsInvalidInvite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &cModel.Circle{}, &cModel.UserCircle{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{Jwt: config.JwtConfig{Secret: "test-secret"}}
	handler := NewHandler(cRepo.NewCircleRepository(db), uRepo.NewUserRepository(db, cfg), nil, nil, storage.NewURLSignerLocal(cfg), cfg)
	ctx, response := familyAccountContext(http.MethodPost, "/api/v1/circles/join?invite_code=expired", uModel.User{ID: 1, CircleID: 1})
	ctx.Request.URL.RawQuery = "invite_code=expired"
	handler.JoinCircle(ctx)
	if response.Code != http.StatusNotFound {
		t.Fatalf("invalid invite status=%d body=%s", response.Code, response.Body)
	}
}
