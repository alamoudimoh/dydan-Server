package migrations

import (
	"context"
	"strings"

	"gorm.io/gorm"
)

// identifyDefaultCategoryRows is the list of seeded defaults this migration
// recognizes, frozen at the time it was written.
//
// It is deliberately a copy rather than a reference to
// repo.DefaultCategoryNames. A migration must keep describing the world as it
// was when it ran: if a later release adds or renames a default, this backfill
// must not silently start claiming different rows in databases that migrated
// long ago. IdentifyDefaultCategories20260926MatchesRuntimeDefaults guards
// against the two lists drifting apart unnoticed.
var identifyDefaultCategoryRows = []string{
	"household",
	"personal",
	"work",
	"school",
	"health",
	"finance",
}

// IdentifyDefaultCategories20260926 gives each already-seeded default category
// a stable per-circle identity.
//
// Default categories used to be recognized only by their display name, which is
// user-editable: renaming "work" to "Day job" made EnsureDefaultCategories
// believe "work" was missing and seed a second row under the original name,
// every time the category list was read. The labels.default_key column records
// which seeded slot a row occupies, so a renamed default is still recognized as
// that same default.
//
// Rows are only adopted when they can be identified with certainty, i.e. when
// the row's current name still equals the default's name. A default the user
// has already renamed cannot be told apart from a category they created
// themselves, so it is left with a NULL key and keeps behaving exactly as it
// does today. Nothing is renamed, recolored, archived or reassigned, and no row
// is deleted: IDs and every existing reference survive untouched.
type IdentifyDefaultCategories20260926 struct{}

func (m IdentifyDefaultCategories20260926) ID() string {
	return "20260926_identify_default_categories"
}

func (m IdentifyDefaultCategories20260926) Description() string {
	return "Identify seeded default categories by a stable per-circle key"
}

// Down is a no-op because the change is purely additive bookkeeping. Clearing
// the column would re-expose the reseeding bug, and dropping it is unnecessary
// since it is never read by anything older.
func (m IdentifyDefaultCategories20260926) Down(context.Context, *gorm.DB) error {
	return nil
}

func (m IdentifyDefaultCategories20260926) Up(ctx context.Context, db *gorm.DB) error {
	// database.Migration AutoMigrates the models before migrations.Run, so on
	// every boot path the column normally already exists. The guard keeps the
	// migration correct when it is applied on its own, as its tests do.
	if !hasExactColumn(db, "labels", "default_key") {
		if err := db.WithContext(ctx).Exec(`ALTER TABLE labels ADD COLUMN default_key TEXT`).Error; err != nil {
			return err
		}
	}
	return identifyExistingDefaultCategories(ctx, db)
}

// identifyExistingDefaultCategoryRow is the subset of labels this migration
// reads and writes. Selecting columns explicitly keeps the statement valid on
// schemas that predate other fields.
type identifyExistingDefaultCategoryRow struct {
	ID         int
	CircleID   *int
	Name       string
	DefaultKey *string
}

func identifyExistingDefaultCategories(ctx context.Context, db *gorm.DB) error {
	var rows []identifyExistingDefaultCategoryRow
	if err := db.WithContext(ctx).Table("labels").
		Select("id, circle_id, name, default_key").
		Where("is_category = ?", true).
		Order("circle_id, id").
		Find(&rows).Error; err != nil {
		return err
	}

	// Group by circle so identity is scoped per circle, matching how the
	// defaults are seeded. A row with no circle cannot carry a per-circle
	// identity and is left alone.
	byCircle := make(map[int][]identifyExistingDefaultCategoryRow)
	for _, row := range rows {
		if row.CircleID == nil {
			continue
		}
		byCircle[*row.CircleID] = append(byCircle[*row.CircleID], row)
	}

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, circleRows := range byCircle {
			// An identity already present is never reassigned, so re-running
			// the migration is a no-op for it.
			claimed := make(map[string]struct{}, len(identifyDefaultCategoryRows))
			for _, row := range circleRows {
				if row.DefaultKey == nil {
					continue
				}
				if identity := normalizeDefaultKey(*row.DefaultKey); identity != "" {
					claimed[identity] = struct{}{}
				}
			}

			for _, name := range identifyDefaultCategoryRows {
				identity := normalizeDefaultKey(name)
				if _, found := claimed[identity]; found {
					continue
				}
				row, found := firstUnidentifiedRowNamed(circleRows, identity)
				if !found {
					continue
				}
				// The `default_key IS NULL` predicate makes the write a no-op if
				// the row was claimed concurrently, so this can never overwrite
				// an identity that is already set.
				if err := tx.Table("labels").
					Where("id = ? AND default_key IS NULL", row.ID).
					Update("default_key", identity).Error; err != nil {
					return err
				}
				claimed[identity] = struct{}{}
			}
		}
		return nil
	})
}

// firstUnidentifiedRowNamed returns the lowest-id row in the circle whose name
// still matches the default and that carries no identity yet. Choosing the
// minimum id explicitly, rather than relying on the caller's query order, keeps
// the choice deterministic when a circle somehow holds two rows with the same
// name: exactly one is adopted and the rest are left untouched.
func firstUnidentifiedRowNamed(rows []identifyExistingDefaultCategoryRow, identity string) (identifyExistingDefaultCategoryRow, bool) {
	var best identifyExistingDefaultCategoryRow
	found := false
	for _, row := range rows {
		if row.DefaultKey != nil {
			continue
		}
		if strings.ToLower(strings.TrimSpace(row.Name)) != identity {
			continue
		}
		if !found || row.ID < best.ID {
			best, found = row, true
		}
	}
	return best, found
}

func normalizeDefaultKey(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func init() {
	Register(IdentifyDefaultCategories20260926{})
}
