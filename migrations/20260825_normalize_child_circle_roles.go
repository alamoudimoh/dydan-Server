package migrations

import (
	"context"
	"fmt"
	"time"

	"gorm.io/gorm"
)

// NormalizeChildCircleRoles20260825 demotes any managed (child) account that
// still holds an elevated circle role (admin/manager) down to "member".
//
// Background: managed child accounts have never been able to exercise admin
// capabilities — auth.ResolveEffectiveRole deliberately ignores stored
// elevated roles for child users, and ChangeMemberRole refuses to grant them.
// However, rows created before that guard existed can still store
// role='admin'/'manager' for a child. Those rows are authorization-inert but
// are serialized to clients verbatim, so the UI can present a child as an
// "Admin" of the household circle — misleading to operators and users alike.
//
// The migration is idempotent (already-member rows are untouched) and safe to
// run concurrently on multiple replicas: it is a single UPDATE whose effect
// is the same no matter how many times it executes within one migration run,
// and migration execution itself is tracked per database by ID.
type NormalizeChildCircleRoles20260825 struct{}

func (m NormalizeChildCircleRoles20260825) ID() string {
	return "20260825_normalize_child_circle_roles"
}

func (m NormalizeChildCircleRoles20260825) Description() string {
	return `Demote managed child accounts holding legacy elevated circle roles (admin/manager) to member. Authorization was never derived from these rows; this only corrects what clients display. Idempotent; safe to re-run.`
}

func (m NormalizeChildCircleRoles20260825) Up(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasTable("user_circles") || !db.Migrator().HasTable("users") {
		// Fresh installations: children are created as members and cannot be
		// elevated, so there is nothing to normalize.
		return nil
	}
	// user_type = 1 is internal/user.UserTypeChild (parent=0, child=1).
	sql := `
UPDATE user_circles
SET role = 'member', updated_at = ?
WHERE role IN ('admin', 'manager')
  AND user_id IN (SELECT id FROM users WHERE user_type = 1)`
	result := db.WithContext(ctx).Exec(sql, time.Now().UTC())
	if result.Error != nil {
		return fmt.Errorf("normalize child circle roles: %w", result.Error)
	}
	return nil
}

// Down cannot reconstruct which elevated role (if any) a child previously
// held, and restoring those rows would reintroduce misleading state with zero
// authorization benefit. Treated as a no-op like other irreversible data
// corrections in this registry.
func (m NormalizeChildCircleRoles20260825) Down(ctx context.Context, db *gorm.DB) error {
	return nil
}

func init() {
	Register(NormalizeChildCircleRoles20260825{})
}
