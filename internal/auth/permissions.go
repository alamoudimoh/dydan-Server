package auth

import (
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
)

type EffectiveRole string

const (
	EffectiveRoleNone  EffectiveRole = "none"
	EffectiveRoleChild EffectiveRole = "child"
	EffectiveRoleAdult EffectiveRole = "adult"
	EffectiveRoleOwner EffectiveRole = "owner"
)

type Permission string

const (
	PermissionManageAccount     Permission = "manage_account"
	PermissionManageSubaccounts Permission = "manage_subaccounts"
	PermissionManageCircle      Permission = "manage_circle"
)

func ResolveEffectiveRole(user *uModel.User, circleRole cModel.Role, active bool) EffectiveRole {
	if user == nil || !active {
		return EffectiveRoleNone
	}
	// A managed account never inherits elevated Circle capabilities from stale
	// or inconsistent membership data.
	if user.UserType == uModel.UserTypeChild {
		return EffectiveRoleChild
	}
	if circleRole == cModel.RoleAdmin {
		return EffectiveRoleOwner
	}
	return EffectiveRoleAdult
}

func HasPermission(role EffectiveRole, permission Permission) bool {
	switch permission {
	case PermissionManageCircle:
		return role == EffectiveRoleOwner
	case PermissionManageAccount, PermissionManageSubaccounts:
		return role == EffectiveRoleOwner || role == EffectiveRoleAdult
	default:
		return false
	}
}

// CapEffectiveCircleRole returns the Circle role the principal may actually
// exercise. Managed (child) accounts never inherit elevated capabilities,
// regardless of stale or inconsistent membership rows: a child whose stored
// role says admin/manager is treated as a plain member by authorization
// gates. Display surfaces should keep using the raw role.
func CapEffectiveCircleRole(user *uModel.User, role cModel.Role) cModel.Role {
	if user != nil && user.UserType == uModel.UserTypeChild && (role == cModel.RoleAdmin || role == cModel.RoleManager) {
		return cModel.RoleMember
	}
	return role
}
