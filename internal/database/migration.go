package database

import (
	"embed"
	"fmt"

	migrate "github.com/rubenv/sql-migrate"
	"gorm.io/gorm"

	"github.com/alamoudimoh/dydan-server/config"
	sModel "github.com/alamoudimoh/dydan-server/external/payment/model"
	"github.com/alamoudimoh/dydan-server/internal/authlimit"
	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	filterModel "github.com/alamoudimoh/dydan-server/internal/filter/model"
	lModel "github.com/alamoudimoh/dydan-server/internal/label/model"
	mModel "github.com/alamoudimoh/dydan-server/internal/meter/model"
	nModel "github.com/alamoudimoh/dydan-server/internal/notifier/model"
	pModel "github.com/alamoudimoh/dydan-server/internal/points"
	progressModel "github.com/alamoudimoh/dydan-server/internal/progress"
	projModel "github.com/alamoudimoh/dydan-server/internal/project/model"
	rewardModel "github.com/alamoudimoh/dydan-server/internal/reward"
	routineModel "github.com/alamoudimoh/dydan-server/internal/routine"
	slModel "github.com/alamoudimoh/dydan-server/internal/shoppinglist/model"
	storageModel "github.com/alamoudimoh/dydan-server/internal/storage/model"
	stModel "github.com/alamoudimoh/dydan-server/internal/subtask/model"
	syncModel "github.com/alamoudimoh/dydan-server/internal/sync/model"
	tModel "github.com/alamoudimoh/dydan-server/internal/thing/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model" // Pure go SQLite driver, checkout https://github.com/glebarez/sqlite for details
	"github.com/alamoudimoh/dydan-server/migrations"
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
		nModel.InboxNotification{},
		uModel.UserPasswordReset{},
		sModel.StripeCustomer{},
		sModel.StripeSubscription{},
		sModel.StripeSession{},
		sModel.StripeInvoice{},
		sModel.RevenueCatEvent{},
		sModel.StripeEvent{},
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
		slModel.ShoppingList{},
		slModel.ShoppingListItem{},
		filterModel.Filter{},
		migrations.Migration{},
		pModel.PointsHistory{},
		rewardModel.Reward{},
		rewardModel.RewardRequest{},
		progressModel.Goal{},
		mModel.Meter{},
		mModel.Reading{},
		mModel.ChoreSchedule{},
		mModel.UpdateReminder{},
		stModel.SubTask{},
		storageModel.StorageFile{},
		storageModel.StorageUsage{},
		chModel.TimeSession{},
		uModel.UserDeviceToken{},
		syncModel.SyncCursor{},
		syncModel.Tombstone{},
		routineModel.Pack{},
		routineModel.Item{},
		routineModel.Application{},
		authlimit.Bucket{},
		authlimit.CapacityGuard{},
	); err != nil {
		return err
	}
	// Legacy Things were user-scoped. Assets are circle-scoped; preserve every
	// existing row by adopting the owner's current circle on upgrade. Visibility
	// remains owner-only unless a maintenance asset explicitly sets is_shared.
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
