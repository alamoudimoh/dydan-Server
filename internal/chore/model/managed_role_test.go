package model

import (
	"testing"
	"time"

	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
)

func TestManagedChildRolesCannotUseAdminOnlyChoreOperations(t *testing.T) {
	chore := Chore{CreatedBy: 1}
	assignedTo := 1
	history := ChoreHistory{CompletedBy: 1, AssignedTo: &assignedTo}
	roles := []*cModel.UserCircleDetail{
		{UserCircle: cModel.UserCircle{UserID: 2, Role: cModel.UserRoleAdmin, IsActive: true}, IsManagedAccount: true},
		{UserCircle: cModel.UserCircle{UserID: 3, Role: cModel.UserRoleManager, IsActive: true}, IsManagedAccount: true},
		{UserCircle: cModel.UserCircle{UserID: 4, Role: cModel.UserRoleManager, IsActive: true}},
		{UserCircle: cModel.UserCircle{UserID: 5, Role: cModel.UserRoleAdmin, IsActive: true}},
	}

	for _, childID := range []int{2, 3} {
		if err := chore.CanEdit(childID, roles, nil); err == nil {
			t.Fatalf("managed child with stale elevated role %d edited another user's chore", childID)
		}
		if chore.CanDeleteHistory(childID, roles, &history) {
			t.Fatalf("managed child with stale elevated role %d deleted another user's history", childID)
		}
	}

	if err := chore.CanEdit(4, roles, nil); err == nil {
		t.Fatal("adult manager gained the historically admin-only chore edit permission")
	}
	if chore.CanDeleteHistory(4, roles, &history) {
		t.Fatal("adult manager gained the historically admin-only history-delete permission")
	}
	if err := chore.CanEdit(5, roles, nil); err != nil {
		t.Fatalf("adult admin lost chore edit permission: %v", err)
	}
	if !chore.CanDeleteHistory(5, roles, &history) {
		t.Fatal("adult admin lost history-delete permission")
	}

	// A timestamp from the normal request path must not affect the role result.
	updatedAt := time.Now().UTC()
	if err := chore.CanEdit(2, roles, &updatedAt); err == nil {
		t.Fatal("managed child bypassed with a request timestamp")
	}
}
