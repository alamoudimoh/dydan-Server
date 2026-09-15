package auth

import (
	"testing"

	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
)

func TestEffectiveRoleAndPermissions(t *testing.T) {
	parent := &uModel.User{UserType: uModel.UserTypeParent}
	child := &uModel.User{UserType: uModel.UserTypeChild}
	tests := []struct {
		name       string
		user       *uModel.User
		circleRole cModel.Role
		active     bool
		want       EffectiveRole
	}{
		{name: "admin is owner", user: parent, circleRole: cModel.RoleAdmin, active: true, want: EffectiveRoleOwner},
		{name: "manager is adult", user: parent, circleRole: cModel.RoleManager, active: true, want: EffectiveRoleAdult},
		{name: "member is adult", user: parent, circleRole: cModel.RoleMember, active: true, want: EffectiveRoleAdult},
		{name: "child overrides stale admin membership", user: child, circleRole: cModel.RoleAdmin, active: true, want: EffectiveRoleChild},
		{name: "inactive has no role", user: parent, circleRole: cModel.RoleAdmin, active: false, want: EffectiveRoleNone},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ResolveEffectiveRole(tt.user, tt.circleRole, tt.active); got != tt.want {
				t.Fatalf("role = %q, want %q", got, tt.want)
			}
		})
	}
	if !HasPermission(EffectiveRoleOwner, PermissionManageCircle) {
		t.Fatal("owner must manage Circle")
	}
	if HasPermission(EffectiveRoleAdult, PermissionManageCircle) || HasPermission(EffectiveRoleChild, PermissionManageAccount) {
		t.Fatal("elevated permissions leaked to adult or child")
	}
	if !HasPermission(EffectiveRoleAdult, PermissionManageAccount) || !HasPermission(EffectiveRoleAdult, PermissionManageSubaccounts) {
		t.Fatal("adult account permissions missing")
	}
}
