package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/daydan-server/internal/circle/repo"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newImpersonationHarness(t *testing.T, principalType uModel.UserType) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file::memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserNotificationTarget{}, &cModel.Circle{}, &cModel.UserCircle{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.Circle{ID: 1, Name: "circle"}).Error; err != nil {
		t.Fatal(err)
	}
	userRepo := uRepo.NewUserRepository(db, &config.Config{})
	router := gin.New()
	router.Use(func(c *gin.Context) {
		// Simulates the authenticated principal hydrated from the DB.
		c.Set(identityKey, &uModel.UserDetails{User: uModel.User{ID: 1, Username: "admin", CircleID: 1, UserType: principalType}})
		c.Next()
	})
	router.Use(ImpersonationMiddleware(userRepo, cRepo.NewCircleRepository(db)))
	router.GET("/probe", func(c *gin.Context) {
		effective, _ := GetEffectiveUser(c)
		actual, _ := GetActualUser(c)
		c.JSON(http.StatusOK, gin.H{"effective": effective.ID, "actual": actual.ID})
	})
	return router, db
}

func seedImpersonationMember(t *testing.T, db *gorm.DB, userID int, role cModel.UserRole, active bool, userType uModel.UserType) {
	t.Helper()
	if err := db.Create(&uModel.User{ID: userID, Username: "user" + time.Now().Format("150405.000000000"), CircleID: 1, UserType: userType}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.UserCircle{UserID: userID, CircleID: 1, Role: role, IsActive: active}).Error; err != nil {
		t.Fatal(err)
	}
}

func doImpersonationRequest(router *gin.Engine, header string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/probe", nil)
	if header != "" {
		req.Header.Set(ImpersonateUserIDHeader, header)
	}
	w := httptest.NewRecorder()
	router.ServeHTTP(w, req)
	return w
}

func TestImpersonationAllowsActiveAdminOfSameCircle(t *testing.T) {
	router, db := newImpersonationHarness(t, uModel.UserTypeParent)
	// Principal (user 1) is an active admin; target is an active member.
	if err := db.Create(&cModel.UserCircle{UserID: 1, CircleID: 1, Role: cModel.UserRoleAdmin, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	seedImpersonationMember(t, db, 2, cModel.UserRoleMember, true, uModel.UserTypeParent)

	w := doImpersonationRequest(router, "2")
	if w.Code != http.StatusOK || w.Body.String() == "" {
		t.Fatalf("active admin should impersonate active member: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestImpersonationDeniedForInactiveAdminMembership(t *testing.T) {
	router, db := newImpersonationHarness(t, uModel.UserTypeParent)
	// Stale row: principal keeps role=admin but membership was deactivated.
	if err := db.Create(&cModel.UserCircle{UserID: 1, CircleID: 1, Role: cModel.UserRoleAdmin, IsActive: false}).Error; err != nil {
		t.Fatal(err)
	}
	seedImpersonationMember(t, db, 2, cModel.UserRoleMember, true, uModel.UserTypeParent)

	w := doImpersonationRequest(router, "2")
	if w.Code != http.StatusForbidden {
		t.Fatalf("inactive admin membership must not preserve impersonation rights: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestImpersonationDeniedForChildPrincipal(t *testing.T) {
	router, db := newImpersonationHarness(t, uModel.UserTypeChild)
	if err := db.Create(&cModel.UserCircle{UserID: 1, CircleID: 1, Role: cModel.UserRoleAdmin, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	seedImpersonationMember(t, db, 2, cModel.UserRoleMember, true, uModel.UserTypeParent)

	w := doImpersonationRequest(router, "2")
	if w.Code != http.StatusForbidden {
		t.Fatalf("managed child must never gain impersonation authority: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestImpersonationDeniedForInactiveTarget(t *testing.T) {
	router, db := newImpersonationHarness(t, uModel.UserTypeParent)
	if err := db.Create(&cModel.UserCircle{UserID: 1, CircleID: 1, Role: cModel.UserRoleAdmin, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	seedImpersonationMember(t, db, 2, cModel.UserRoleMember, false, uModel.UserTypeParent)

	w := doImpersonationRequest(router, "2")
	if w.Code != http.StatusForbidden {
		t.Fatalf("impersonating a deactivated member must be denied: status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestImpersonationDeniedAcrossCircles(t *testing.T) {
	router, db := newImpersonationHarness(t, uModel.UserTypeParent)
	if err := db.Create(&cModel.UserCircle{UserID: 1, CircleID: 1, Role: cModel.UserRoleAdmin, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	// Target belongs to another circle entirely.
	seedImpersonationMember(t, db, 2, cModel.UserRoleMember, true, uModel.UserTypeParent)
	if err := db.Model(&cModel.UserCircle{}).Where("user_id = ?", 2).Update("circle_id", 99).Error; err != nil {
		t.Fatal(err)
	}

	w := doImpersonationRequest(router, "2")
	if w.Code != http.StatusForbidden {
		t.Fatalf("cross-circle impersonation must be impossible: status=%d body=%s", w.Code, w.Body.String())
	}
}
