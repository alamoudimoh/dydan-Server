package migrations

import (
	"context"

	"gorm.io/gorm"
)

// ClassifyCategories20260813 repairs the initial Category release, which used
// every Circle label as a Category without persisting the entity's role.
type ClassifyCategories20260813 struct{}

func (m ClassifyCategories20260813) ID() string {
	return "20260813_classify_categories"
}

func (m ClassifyCategories20260813) Description() string {
	return "Separate genuine Categories from legacy Labels"
}

func (m ClassifyCategories20260813) Down(context.Context, *gorm.DB) error {
	return nil
}

func (m ClassifyCategories20260813) Up(ctx context.Context, db *gorm.DB) error {
	// Category references and archive state are definitive evidence. The first
	// Category API also created unassigned rows with an empty color, whereas the
	// Label UI always assigned a color and migrated Labels retained chore_labels.
	// Keeping the row in labels preserves its ID and every existing relationship.
	return db.WithContext(ctx).Exec(`
		UPDATE labels
		SET is_category = TRUE
		WHERE is_category = FALSE
		  AND (
			archived_at IS NOT NULL
			OR EXISTS (SELECT 1 FROM chores WHERE chores.category_id = labels.id)
			OR EXISTS (SELECT 1 FROM things WHERE things.category_id = labels.id)
			OR EXISTS (SELECT 1 FROM routine_items WHERE routine_items.category_id = labels.id)
			OR (
				COALESCE(labels.color, '') = ''
				AND NOT EXISTS (SELECT 1 FROM chore_labels WHERE chore_labels.label_id = labels.id)
			)
		  )
	`).Error
}

func init() {
	Register(ClassifyCategories20260813{})
}
