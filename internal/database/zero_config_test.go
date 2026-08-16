package database

import (
	"context"
	"testing"

	"github.com/alamoudimoh/daydan-server/config"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/alamoudimoh/daydan-server/migrations"
)

func TestDefaultSQLiteMigrationAndRestartPreserveData(t *testing.T) {
	dataDir := t.TempDir()
	for _, name := range []string{
		"DAYDAN_ENV", "DAYDAN_CONFIG_FILE", "DAYDAN_DATA_DIR", "DAYDAN_JWT_SECRET", "DAYDAN_SQLITE_PATH",
		"DT_ENV", "DT_CONFIG_FILE", "TASKIER_CONFIG_FILE", "DT_JWT_SECRET",
		"TASKIER_JWT_SECRET", "DT_SQLITE_PATH", "TASKIER_SQLITE_PATH",
	} {
		t.Setenv(name, "")
	}
	t.Setenv("DAYDAN_DATA_DIR", dataDir)

	cfg, err := config.LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	db, err := NewDatabase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if err := Migration(db); err != nil {
		t.Fatal(err)
	}
	if err := migrations.Run(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if err := MigrationScripts(db, cfg); err != nil {
		t.Fatal(err)
	}
	user := uModel.User{Username: "persistent-user", Email: "persistent@example.test", DisplayName: "Persistent"}
	if err := db.WithContext(context.Background()).Create(&user).Error; err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}

	restarted, err := NewDatabase(cfg)
	if err != nil {
		t.Fatal(err)
	}
	var saved uModel.User
	if err := restarted.Where("username = ?", "persistent-user").First(&saved).Error; err != nil {
		t.Fatal(err)
	}
	if saved.ID != user.ID {
		t.Fatalf("restart loaded user ID %d, want %d", saved.ID, user.ID)
	}
}
