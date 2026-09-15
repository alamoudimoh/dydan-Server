package circle_test

import (
	"testing"

	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
)

// Elevated stored roles must not confer authority on inactive memberships or
// managed child accounts, matching auth.ResolveEffectiveRole semantics.
func TestUserCircleDetailIsManagerOrAdminCapsInactiveAndManagedRows(t *testing.T) {
	active := cModel.UserCircleDetail{UserCircle: cModel.UserCircle{Role: cModel.UserRoleAdmin, IsActive: true}}
	if !active.IsManagerOrAdmin() {
		t.Fatal("active admin row must confer authority")
	}

	inactive := active
	inactive.IsActive = false
	if inactive.IsManagerOrAdmin() {
		t.Fatal("inactive admin row must NOT confer authority")
	}

	child := active
	child.IsManagedAccount = true
	if child.IsManagerOrAdmin() {
		t.Fatal("managed child row must NOT confer authority even with admin role")
	}
}
