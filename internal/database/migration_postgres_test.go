package database

import (
	"context"
	"os"
	"testing"

	"github.com/alamoudimoh/dydan-server/config"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/alamoudimoh/dydan-server/migrations"
)

// TestMigrationScriptsPostgres runs the full schema + script migration chain
// against a live PostgreSQL instance and proves the child-role normalization
// migration applies cleanly there too. Skipped unless DYDAN_TEST_POSTGRES_DSN
// is set.
func TestMigrationScriptsPostgres(t *testing.T) {
	dsn := os.Getenv("DYDAN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DYDAN_TEST_POSTGRES_DSN not set; skipping live PostgreSQL migration coverage")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := Migration(db); err != nil {
		t.Fatalf("AutoMigrate: %v", err)
	}
	cfg := &config.Config{}
	cfg.Database.Type = "postgres"
	if err := MigrationScripts(db, cfg); err != nil {
		t.Fatalf("MigrationScripts: %v", err)
	}
	// Second run must be a no-op (idempotent / already-applied skipped).
	if err := MigrationScripts(db, cfg); err != nil {
		t.Fatalf("MigrationScripts second run: %v", err)
	}

	// Seed legacy damage after the chain ran and confirm the migration's
	// effect is reproducible by direct execution (multi-replica safety: the
	// statement is order- and repetition-independent).
	parent := uModel.User{Username: "pg-parent", UserType: uModel.UserTypeParent}
	child := uModel.User{Username: "pg-child", UserType: uModel.UserTypeChild}
	for _, u := range []*uModel.User{&parent, &child} {
		if err := db.Create(u).Error; err != nil {
			t.Skipf("seed user failed (%v); database not pristine", err)
		}
	}
	circle := cModel.Circle{Name: "pg-home", CreatedBy: parent.ID}
	if err := db.Create(&circle).Error; err != nil {
		t.Skipf("seed circle failed (%v)", err)
	}
	row := cModel.UserCircle{UserID: child.ID, CircleID: circle.ID, Role: cModel.UserRoleAdmin, IsActive: true}
	if err := db.Create(&row).Error; err != nil {
		t.Skipf("seed membership failed (%v)", err)
	}
	m := migrationsRegistryLookup("20260825_normalize_child_circle_roles")
	if m == nil {
		t.Fatal("child-role normalization migration not registered")
	}
	if err := m.Up(context.Background(), db); err != nil {
		t.Fatalf("Up on postgres: %v", err)
	}
	var role string
	if err := db.Raw(`SELECT role FROM user_circles WHERE user_id = ?`, child.ID).Scan(&role).Error; err != nil {
		t.Fatal(err)
	}
	if role != "member" {
		t.Fatalf("child still holds role %q on postgres", role)
	}
}

// migrationsRegistryLookup finds a registered migration by ID for targeted
// re-execution in tests.
func migrationsRegistryLookup(id string) migrations.MigrationScript {
	for _, m := range migrations.All() {
		if m.ID() == id {
			return m
		}
	}
	return nil
}
