package repo

import (
	"context"
	"errors"
	"testing"

	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestChangeUserRolePreservesLastActiveAdmin(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&cModel.UserCircle{}); err != nil {
		t.Fatal(err)
	}
	members := []cModel.UserCircle{
		{UserID: 1, CircleID: 7, Role: cModel.UserRoleAdmin, IsActive: true},
		{UserID: 2, CircleID: 7, Role: cModel.UserRoleMember, IsActive: true},
	}
	if err := db.Create(&members).Error; err != nil {
		t.Fatal(err)
	}
	repository := NewCircleRepository(db)
	if err := repository.ChangeUserRole(context.Background(), 7, 1, cModel.RoleMember); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("last admin demotion error = %v, want ErrLastAdmin", err)
	}
	if err := repository.ChangeUserRole(context.Background(), 7, 2, cModel.RoleAdmin); err != nil {
		t.Fatal(err)
	}
	if err := repository.ChangeUserRole(context.Background(), 7, 1, cModel.RoleMember); err != nil {
		t.Fatalf("demotion with another admin failed: %v", err)
	}
}

func TestDeleteMemberPreservesLastActiveAdmin(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&cModel.UserCircle{}); err != nil {
		t.Fatal(err)
	}
	admin := cModel.UserCircle{UserID: 11, CircleID: 8, Role: cModel.UserRoleAdmin, IsActive: true}
	if err := db.Create(&admin).Error; err != nil {
		t.Fatal(err)
	}
	repository := NewCircleRepository(db)
	if err := repository.DeleteMemberByID(context.Background(), 8, 11); !errors.Is(err, ErrLastAdmin) {
		t.Fatalf("delete last admin error = %v, want ErrLastAdmin", err)
	}
	var count int64
	db.Model(&cModel.UserCircle{}).Where("circle_id = ? AND user_id = ?", 8, 11).Count(&count)
	if count != 1 {
		t.Fatalf("last admin membership count = %d, want 1", count)
	}
}
