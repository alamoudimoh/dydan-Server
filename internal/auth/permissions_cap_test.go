package auth

import (
	"testing"

	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
)

func TestCapEffectiveCircleRoleCapsManagedAccounts(t *testing.T) {
	child := &uModel.User{ID: 2, UserType: uModel.UserTypeChild}
	parent := &uModel.User{ID: 3, UserType: uModel.UserTypeParent}

	if got := CapEffectiveCircleRole(child, cModel.RoleAdmin); got != cModel.RoleMember {
		t.Fatalf("child with stale admin row must be capped to member, got %q", got)
	}
	if got := CapEffectiveCircleRole(child, cModel.RoleManager); got != cModel.RoleMember {
		t.Fatalf("child with manager row must be capped to member, got %q", got)
	}
	if got := CapEffectiveCircleRole(parent, cModel.RoleAdmin); got != cModel.RoleAdmin {
		t.Fatalf("parent admin must keep admin, got %q", got)
	}
	if got := CapEffectiveCircleRole(nil, cModel.RoleAdmin); got != cModel.RoleAdmin {
		t.Fatalf("nil user should pass through unchanged, got %q", got)
	}
}
