package circle

import (
	"testing"

	cModel "donetick.com/core/internal/circle/model"
)

func TestCanResetPointsRequiresAdminRole(t *testing.T) {
	members := []*cModel.UserCircleDetail{
		{UserCircle: cModel.UserCircle{UserID: 1, Role: cModel.UserRoleAdmin}},
		{UserCircle: cModel.UserCircle{UserID: 2, Role: cModel.UserRoleManager}},
		{UserCircle: cModel.UserCircle{UserID: 3, Role: cModel.UserRoleMember}},
	}
	if !canResetPoints(members, 1) {
		t.Fatal("admin should be allowed to reset points")
	}
	if canResetPoints(members, 2) {
		t.Fatal("manager should not be allowed to reset points")
	}
	if canResetPoints(members, 3) {
		t.Fatal("member should not be allowed to reset points")
	}
	if canResetPoints(members, 99) {
		t.Fatal("non-member should not be allowed to reset points")
	}
}
