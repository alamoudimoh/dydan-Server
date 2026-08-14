package auth

import (
	cModel "donetick.com/core/internal/circle/model"
	uModel "donetick.com/core/internal/user/model"
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
