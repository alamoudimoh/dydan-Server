package migrations

import (
	"context"
	"path/filepath"
	"testing"

	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newChildRoleFixtureDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(
		filepath.ToSlash(filepath.Join(t.TempDir(), "child-roles.db"))+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &cModel.Circle{}, &cModel.UserCircle{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestNormalizeChildCircleRolesMigration proves the legacy-role cleanup:
// children holding stored admin/manager rows are demoted to member, parents
// and regular members keep their roles, and re-running is a no-op.
func TestNormalizeChildCircleRolesMigration(t *testing.T) {
	db := newChildRoleFixtureDB(t)

	parent := uModel.User{Username: "parent", UserType: uModel.UserTypeParent}
	child := uModel.User{Username: "child", UserType: uModel.UserTypeChild, ParentUserID: &[]int{1}[0]}
	member := uModel.User{Username: "member", UserType: uModel.UserTypeParent}
	for _, u := range []*uModel.User{&parent, &child, &member} {
		if err := db.Create(u).Error; err != nil {
			t.Fatalf("seed user: %v", err)
		}
	}

	circle := cModel.Circle{Name: "home", CreatedBy: parent.ID}
	if err := db.Create(&circle).Error; err != nil {
		t.Fatalf("seed circle: %v", err)
	}

	rows := []cModel.UserCircle{
		// Legacy damage: child stored as admin/manager (authorization-inert
		// but displayed raw to clients).
		{UserID: child.ID, CircleID: circle.ID, Role: cModel.UserRoleAdmin, IsActive: true},
		{UserID: member.ID, CircleID: circle.ID, Role: cModel.UserRoleManager, IsActive: true},
		{UserID: parent.ID, CircleID: circle.ID, Role: cModel.UserRoleAdmin, IsActive: true},
	}
	for i := range rows {
		if err := db.Create(&rows[i]).Error; err != nil {
			t.Fatalf("seed membership: %v", err)
		}
	}

	m := NormalizeChildCircleRoles20260825{}
	ctx := context.Background()
	if err := m.Up(ctx, db); err != nil {
		t.Fatalf("Up: %v", err)
	}

	var roles map[int]string
	expect := func() {
		t.Helper()
		var after []cModel.UserCircle
		if err := db.Order("user_id").Find(&after).Error; err != nil {
			t.Fatal(err)
		}
		roles = map[int]string{}
		for _, r := range after {
			roles[r.UserID] = string(r.Role)
		}
	}
	expect()

	if roles[child.ID] != string(cModel.UserRoleMember) {
		t.Fatalf("child role must be demoted to member, got %q", roles[child.ID])
	}
	if roles[member.ID] != string(cModel.UserRoleManager) || roles[parent.ID] != string(cModel.UserRoleAdmin) {
		t.Fatalf("adult roles must be untouched: member=%q parent=%q", roles[member.ID], roles[parent.ID])
	}

	// Idempotency: re-run changes nothing.
	if err := m.Up(ctx, db); err != nil {
		t.Fatalf("re-run: %v", err)
	}
	expect()
	if roles[child.ID] != string(cModel.UserRoleMember) ||
		roles[parent.ID] != string(cModel.UserRoleAdmin) {
		t.Fatal("second run must be a no-op")
	}
}
