package migrations

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func TestClassifyCategoriesUpgradePreservesLegacyData(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file::memory:?cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE labels (id INTEGER PRIMARY KEY, name TEXT, color TEXT, circle_id INTEGER, created_by INTEGER, archived_at datetime, is_category numeric NOT NULL DEFAULT false)`,
		`CREATE TABLE chores (id INTEGER PRIMARY KEY, name TEXT, circle_id INTEGER, category_id INTEGER)`,
		`CREATE TABLE chore_labels (chore_id INTEGER, label_id INTEGER, user_id INTEGER)`,
		`CREATE TABLE chore_histories (id INTEGER PRIMARY KEY, chore_id INTEGER, status INTEGER, points INTEGER)`,
		`CREATE TABLE things (id INTEGER PRIMARY KEY, category_id INTEGER)`,
		`CREATE TABLE routine_items (id INTEGER PRIMARY KEY, category_id INTEGER)`,
		`INSERT INTO labels VALUES (1, '01_test', '#26a69a', 1, 1, NULL, false), (2, '02_test', '#b39ddb', 1, 1, NULL, false), (3, 'legacy', '', 1, 1, NULL, false), (4, 'car', '', 1, 1, NULL, false)`,
		`INSERT INTO chores VALUES (1, '01_test', 1, NULL), (2, '02_test', 1, NULL)`,
		`INSERT INTO chore_labels VALUES (1, 1, 1), (1, 3, 1), (2, 2, 1), (2, 3, 1)`,
		`INSERT INTO chore_histories VALUES (1, 1, 1, 17), (2, 2, 1, 10)`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := (ClassifyCategories20260813{}).Up(context.Background(), db); err != nil {
		t.Fatal(err)
	}

	var categoryNames []string
	if err := db.Table("labels").Where("is_category = ?", true).Order("id").Pluck("name", &categoryNames).Error; err != nil {
		t.Fatal(err)
	}
	if len(categoryNames) != 1 || categoryNames[0] != "car" {
		t.Fatalf("unexpected categories after upgrade: %v", categoryNames)
	}
	var chores, histories, relationships int64
	db.Table("chores").Count(&chores)
	db.Table("chore_histories").Count(&histories)
	db.Table("chore_labels").Count(&relationships)
	if chores != 2 || histories != 2 || relationships != 4 {
		t.Fatalf("legacy data changed: chores=%d histories=%d relationships=%d", chores, histories, relationships)
	}
	var assigned int64
	db.Table("chores").Where("category_id IS NOT NULL").Count(&assigned)
	if assigned != 0 {
		t.Fatalf("legacy chores received categories: %d", assigned)
	}
}
