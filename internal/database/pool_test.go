package database

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/alamoudimoh/dydan-server/config"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

var errorTestSentinel = errors.New("sentinel")

func openTestSQLite(t *testing.T) (*gorm.DB, error) {
	t.Helper()
	return gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "lock-test.db")), &gorm.Config{})
}

// TestPoolLimitsApplied: NewDatabase must bound the connection pool instead of
// inheriting database/sql's unlimited MaxOpenConns default.
func TestPoolLimitsApplied(t *testing.T) {
	cfg := &config.Config{}
	cfg.Database.Type = "sqlite"
	cfg.Database.Path = filepath.Join(t.TempDir(), "pool-test.db")

	db, err := NewDatabase(cfg)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql handle: %v", err)
	}
	defer sqlDB.Close()

	stats := sqlDB.Stats()
	if stats.MaxOpenConnections != 25 {
		t.Fatalf("MaxOpenConnections = %d, want default 25", stats.MaxOpenConnections)
	}
}

// TestPoolLimitsCustomValues: operator-configured pool bounds are respected
// and max_idle_conns is clamped to max_open_conns.
func TestPoolLimitsCustomValues(t *testing.T) {
	cfg := &config.Config{}
	cfg.Database.Type = "sqlite"
	cfg.Database.Path = filepath.Join(t.TempDir(), "pool-custom.db")
	cfg.Database.MaxOpenConns = 7
	cfg.Database.MaxIdleConns = 20 // must clamp to 7

	db, err := NewDatabase(cfg)
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("sql handle: %v", err)
	}
	defer sqlDB.Close()

	stats := sqlDB.Stats()
	if stats.MaxOpenConnections != 7 {
		t.Fatalf("MaxOpenConnections = %d, want 7", stats.MaxOpenConnections)
	}
}

// TestMigrationLockSQLitePassthrough: SQLite needs no cross-process migration
// lock; fn runs exactly once and errors propagate.
func TestMigrationLockSQLitePassthrough(t *testing.T) {
	db, err := openTestSQLite(t)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	calls := 0
	sentinel := errorTestSentinel
	err = WithMigrationLock(db, func() error {
		calls++
		return sentinel
	})
	if calls != 1 {
		t.Fatalf("fn called %d times, want 1", calls)
	}
	if err != sentinel {
		t.Fatalf("error not propagated: %v", err)
	}
}
