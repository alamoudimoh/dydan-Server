package database

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/alamoudimoh/dydan-server/config"
	rewardModel "github.com/alamoudimoh/dydan-server/internal/reward"
	tModel "github.com/alamoudimoh/dydan-server/internal/thing/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/alamoudimoh/dydan-server/migrations"
)

func TestMigrationScriptsSQLite(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test_dydan.db")
	db, err := gorm.Open(sqlite.Open(dbPath), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}

	if err := Migration(db); err != nil {
		t.Fatalf("AutoMigrate failed: %v", err)
	}
	if !db.Migrator().HasTable(&rewardModel.Reward{}) || !db.Migrator().HasTable(&rewardModel.RewardRequest{}) || !db.Migrator().HasColumn("points_histories", "reward_request_id") {
		t.Fatal("rewards schema was not created")
	}
	if !db.Migrator().HasColumn("things", "is_shared") {
		t.Fatal("thing visibility schema was not created")
	}
	if err := Migration(db); err != nil {
		t.Fatalf("AutoMigrate second run failed: %v", err)
	}
	// main.go ignores the error from migrations.Run; some legacy Go migrations
	// (e.g. 20241123_migrate_label_to_labels_v2) fail on a fresh database.
	_ = migrations.Run(context.Background(), db)

	cfg := &config.Config{}
	cfg.Database.Type = "sqlite"
	if err := MigrationScripts(db, cfg); err != nil {
		t.Fatalf("MigrationScripts failed: %v", err)
	}

	// Running again must be idempotent (already-applied migrations skipped).
	if err := MigrationScripts(db, cfg); err != nil {
		t.Fatalf("MigrationScripts second run failed: %v", err)
	}
}

func TestMigrationBackfillsLegacyThingCircleAndIsIdempotent(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "legacy.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &tModel.Thing{}); err != nil {
		t.Fatal(err)
	}
	user := uModel.User{Username: "legacy", CircleID: 42}
	if err := db.Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	thing := tModel.Thing{UserID: user.ID, Name: "Existing filter", Type: "text"}
	if err := db.Create(&thing).Error; err != nil {
		t.Fatal(err)
	}
	if err := Migration(db); err != nil {
		t.Fatal(err)
	}
	if err := Migration(db); err != nil {
		t.Fatal(err)
	}
	var migrated tModel.Thing
	if err := db.First(&migrated, thing.ID).Error; err != nil {
		t.Fatal(err)
	}
	if migrated.CircleID != 42 || migrated.Name != thing.Name || migrated.IsShared {
		t.Fatalf("legacy thing was not preserved: %#v", migrated)
	}
}
