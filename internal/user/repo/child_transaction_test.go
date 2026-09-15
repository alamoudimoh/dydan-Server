package user

import (
	"context"
	"testing"

	"github.com/alamoudimoh/dydan-server/config"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestCreateChildWithMembershipRollsBackOnMembershipFailure(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// Intentionally omit user_circles so the second write fails.
	if err := db.AutoMigrate(&uModel.User{}); err != nil {
		t.Fatal(err)
	}
	repository := NewUserRepository(db, &config.Config{})
	child := &uModel.User{Username: "parent_child", UserType: uModel.UserTypeChild, CircleID: 9}
	_, err = repository.CreateChildWithMembership(context.Background(), child, &cModel.UserCircle{CircleID: 9, Role: cModel.UserRoleMember, IsActive: true})
	if err == nil {
		t.Fatal("expected membership insertion to fail")
	}
	var count int64
	if err := db.Model(&uModel.User{}).Where("username = ?", child.Username).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("orphaned child rows = %d, want 0", count)
	}
}
