package circle

import (
	"testing"

	"github.com/alamoudimoh/dydan-server/internal/auth"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
)

func TestCanResetPointsRequiresAdminRole(t *testing.T) {
	members := []*cModel.UserCircleDetail{
		{UserCircle: cModel.UserCircle{UserID: 1, Role: cModel.UserRoleAdmin, IsActive: true}},
		{UserCircle: cModel.UserCircle{UserID: 2, Role: cModel.UserRoleManager, IsActive: true}},
		{UserCircle: cModel.UserCircle{UserID: 3, Role: cModel.UserRoleMember, IsActive: true}},
	}
	parent := func(id int) *uModel.UserDetails {
		return &uModel.UserDetails{User: uModel.User{ID: id, UserType: uModel.UserTypeParent}}
	}
	if !hasCirclePermission(parent(1), members, auth.PermissionManageCircle) {
		t.Fatal("admin should be allowed to reset points")
	}
	if hasCirclePermission(parent(2), members, auth.PermissionManageCircle) {
		t.Fatal("manager should not be allowed to reset points")
	}
	if hasCirclePermission(parent(3), members, auth.PermissionManageCircle) {
		t.Fatal("member should not be allowed to reset points")
	}
	if hasCirclePermission(parent(99), members, auth.PermissionManageCircle) {
		t.Fatal("non-member should not be allowed to reset points")
	}
	managedChild := &uModel.UserDetails{User: uModel.User{ID: 1, UserType: uModel.UserTypeChild}}
	if hasCirclePermission(managedChild, members, auth.PermissionManageCircle) {
		t.Fatal("managed child must not inherit a stale admin membership")
	}
}
