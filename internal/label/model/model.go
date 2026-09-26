package model

import "time"

type Label struct {
	ID         int        `json:"id" gorm:"primary_key"`
	Name       string     `json:"name" gorm:"column:name"`
	Color      string     `json:"color" gorm:"column:color"`
	CircleID   *int       `json:"-" gorm:"column:circle_id"`
	CreatedBy  int        `json:"created_by" gorm:"column:created_by"`
	ArchivedAt *time.Time `json:"archivedAt,omitempty" gorm:"column:archived_at;index"`
	IsCategory bool       `json:"-" gorm:"column:is_category;not null;default:false;index"`

	// DefaultKey is the stable identity of a server-seeded default category
	// within its Circle, and is NULL for every user-created category.
	//
	// It exists because a default's display name is editable. Identifying
	// defaults by name meant that renaming "work" to "Day job" left the next
	// EnsureDefaultCategories believing "work" was missing, so it seeded a
	// second row under the old name. The key is assigned once, when the
	// default is seeded or when a pre-existing row is adopted by the
	// 20260926_identify_default_categories migration, and no write path in the
	// category API may change it: rename and recolor go through an explicit
	// column map and archive only touches archived_at.
	//
	// json:"-" keeps it out of every API response, so the /api/v1/categories
	// contract is unchanged and this remains server-internal bookkeeping.
	DefaultKey *string `json:"-" gorm:"column:default_key;index"`
}

type LabelReq struct {
	LabelID int `json:"id" binding:"required,gt=0"`
}
