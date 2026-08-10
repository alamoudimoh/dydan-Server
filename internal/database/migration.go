package database

import (
	"embed"
	"fmt"

	migrate "github.com/rubenv/sql-migrate"
	"gorm.io/gorm"

	"donetick.com/core/config"
	sModel "donetick.com/core/external/payment/model"
	chModel "donetick.com/core/internal/chore/model"
	cModel "donetick.com/core/internal/circle/model"
	filterModel "donetick.com/core/internal/filter/model"
	lModel "donetick.com/core/internal/label/model"
	mModel "donetick.com/core/internal/meter/model"
	nModel "donetick.com/core/internal/notifier/model"
	pModel "donetick.com/core/internal/points"
	projModel "donetick.com/core/internal/project/model"
	rewardModel "donetick.com/core/internal/reward"
	storageModel "donetick.com/core/internal/storage/model"
	stModel "donetick.com/core/internal/subtask/model"
	syncModel "donetick.com/core/internal/sync/model"
	tModel "donetick.com/core/internal/thing/model"
	uModel "donetick.com/core/internal/user/model" // Pure go SQLite driver, checkout https://github.com/glebarez/sqlite for details
	"donetick.com/core/migrations"
)

//go:embed migrations/*.sql
var embeddedMigrations embed.FS

func Migration(db *gorm.DB) error {
	if err := db.AutoMigrate(uModel.User{}, chModel.Chore{},
		chModel.ChoreHistory{},
		cModel.Circle{},
		cModel.UserCircle{},
		chModel.ChoreAssignees{},
		nModel.Notification{},
		uModel.UserPasswordReset{},
		sModel.StripeCustomer{},
		sModel.StripeSubscription{},
		sModel.StripeSession{},
		sModel.StripeInvoice{},
		sModel.RevenueCatEvent{},
		sModel.RevenueCatSubscription{},
		sModel.Subscription{},
		uModel.MFASession{},
		uModel.UserSession{},
		tModel.Thing{},
		tModel.ThingChore{},
		tModel.ThingHistory{},
		uModel.APIToken{},
		uModel.UserNotificationTarget{},
		lModel.Label{},
		chModel.ChoreLabels{},
		projModel.Project{},
		filterModel.Filter{},
		migrations.Migration{},
		pModel.PointsHistory{},
		rewardModel.Reward{},
		rewardModel.RewardRequest{},
		mModel.Meter{},
		mModel.Reading{},
		mModel.ChoreSchedule{},
		stModel.SubTask{},
		storageModel.StorageFile{},
		storageModel.StorageUsage{},
		chModel.TimeSession{},
		uModel.UserDeviceToken{},
		syncModel.SyncCursor{},
		syncModel.Tombstone{},
	); err != nil {
		return err
	}
	// Legacy Things were user-scoped. Assets are circle-scoped; preserve every
	// existing row by adopting the owner's current circle on upgrade.
	if err := db.Exec(`UPDATE things SET circle_id = (SELECT circle_id FROM users WHERE users.id = things.user_id) WHERE (circle_id IS NULL OR circle_id = 0) AND user_id IS NOT NULL`).Error; err != nil {
		return err
	}

	return nil
}

func MigrationScripts(gormDB *gorm.DB, cfg *config.Config) error {
	migrations := &migrate.EmbedFileSystemMigrationSource{
		FileSystem: embeddedMigrations,
		Root:       "migrations",
	}

	var dialect string
	switch cfg.Database.Type {
	case "postgres":
		dialect = "postgres"
	case "sqlite":
		dialect = "sqlite3"
	default:
		return fmt.Errorf("unsupported database type: %s", cfg.Database.Type)
	}

	db, err := gormDB.DB()
	if err != nil {
		return err
	}
	var n int
	n, err = migrate.Exec(db, dialect, migrations, migrate.Up)
	if err != nil {
		return err
	}
	fmt.Printf("Applied %d migrations!\n", n)
	return nil
}
