package database

import (
	"context"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func testPostgresDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("DAYDAN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DAYDAN_TEST_POSTGRES_DSN not set; skipping live PostgreSQL coverage")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	t.Cleanup(func() {
		if sqlDB, err := db.DB(); err == nil {
			sqlDB.Close()
		}
	})
	return db
}

// TestMigrationLockPostgresRunsAndReleases: the advisory lock is acquired,
// fn runs, and the lock is genuinely released afterwards (a second holder can
// take it immediately).
func TestMigrationLockPostgresRunsAndReleases(t *testing.T) {
	db := testPostgresDB(t)

	var ran atomic.Int32
	if err := WithMigrationLock(db, func() error { ran.Add(1); return nil }); err != nil {
		t.Fatalf("WithMigrationLock: %v", err)
	}
	if ran.Load() != 1 {
		t.Fatalf("fn ran %d times", ran.Load())
	}

	// The lock must be free now: a raw session takes it without waiting.
	sqlDB, _ := db.DB()
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("conn: %v", err)
	}
	defer conn.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey)
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("lock re-acquisition failed: %v", err)
		}
	case <-ctx.Done():
		t.Fatal("migration lock was NOT released after WithMigrationLock returned")
	}
}

// TestMigrationLockPostgresSerializes: while another session holds the
// advisory lock, WithMigrationLock blocks instead of racing; it proceeds once
// the holder releases. This is the multi-replica startup guarantee.
func TestMigrationLockPostgresSerializes(t *testing.T) {
	db := testPostgresDB(t)
	sqlDB, _ := db.DB()

	holder, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatalf("holder conn: %v", err)
	}
	defer holder.Close()
	if _, err := holder.ExecContext(context.Background(), "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		t.Fatalf("hold lock: %v", err)
	}

	var entered atomic.Bool
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		_ = WithMigrationLock(db, func() error { entered.Store(true); return nil })
	}()

	// While held: fn must not have run.
	time.Sleep(300 * time.Millisecond)
	if entered.Load() {
		t.Fatal("WithMigrationLock entered while another session held the lock")
	}

	if _, err := holder.ExecContext(context.Background(), "SELECT pg_advisory_unlock($1)", migrationLockKey); err != nil {
		t.Fatalf("unlock: %v", err)
	}
	select {
	case <-finished:
	case <-time.After(5 * time.Second):
		t.Fatal("WithMigrationLock did not proceed after the holder released")
	}
}
