package database

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// migrationLockKey is the fixed PostgreSQL advisory-lock key serializing the
// migration phase across replicas. A constant (rather than hashtext of a
// name) keeps every deployment on the same well-known lock domain.
const migrationLockKey = 0x64616E6D // 'danm'

// migrationLockTimeout bounds how long a replica waits for another replica's
// migration run. Migrations are expected to finish in minutes (fx.StartTimeout
// allows 15); a wait longer than this means the holder is wedged and failing
// loudly is better than blocking startup indefinitely.
const migrationLockTimeout = 10 * time.Minute

// WithMigrationLock runs fn while holding an exclusive, process-spanning
// migration lock.
//
// PostgreSQL: a session-level advisory lock taken on a dedicated connection,
// so two replicas starting concurrently cannot both enter the check-then-apply
// migration phase and race (e.g. both passing the "already applied?" probe and
// double-applying a data migration, or colliding on the migrations bookkeeping
// row). SQLite needs no cross-process coordination: writes are serialized by
// the database itself and busy_timeout absorbs contention, so fn runs directly.
//
// The lock is always released — including on panic — before returning.
func WithMigrationLock(db *gorm.DB, fn func() error) error {
	if db == nil {
		return fmt.Errorf("migration lock: nil database handle")
	}
	if db.Dialector.Name() != "postgres" {
		return fn()
	}

	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("migration lock: get sql handle: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), migrationLockTimeout)
	defer cancel()

	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migration lock: acquire connection: %w", err)
	}
	defer conn.Close()

	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", migrationLockKey); err != nil {
		return fmt.Errorf("migration lock: acquire advisory lock: %w", err)
	}
	defer func() {
		// Best-effort unlock; closing the session releases it regardless.
		unlockCtx, unlockCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer unlockCancel()
		_, _ = conn.ExecContext(unlockCtx, "SELECT pg_advisory_unlock($1)", migrationLockKey)
	}()

	return fn()
}
