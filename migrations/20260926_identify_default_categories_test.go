package migrations

import (
	"context"
	"fmt"
	"testing"

	lModel "github.com/alamoudimoh/dydan-server/internal/label/model"
	lRepo "github.com/alamoudimoh/dydan-server/internal/label/repo"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// identifyFixture creates a labels table in the pre-migration shape, i.e.
// without default_key, so the tests exercise the real upgrade path including
// the ALTER TABLE.
func identifyFixture(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	statements := []string{
		`CREATE TABLE labels (id INTEGER PRIMARY KEY, name TEXT, color TEXT, circle_id INTEGER, created_by INTEGER, archived_at datetime, is_category numeric NOT NULL DEFAULT false)`,
		`CREATE TABLE chores (id INTEGER PRIMARY KEY, name TEXT, circle_id INTEGER, category_id INTEGER)`,
	}
	for _, statement := range statements {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}
	return db
}

type identifiedRow struct {
	ID         int
	Name       string
	Color      string
	CircleID   *int
	ArchivedAt *string
	DefaultKey *string
}

func readRows(t *testing.T, db *gorm.DB) []identifiedRow {
	t.Helper()
	var rows []identifiedRow
	if err := db.Table("labels").
		Select("id, name, color, circle_id, archived_at, default_key").
		Order("id").Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	return rows
}

func keyOf(row identifiedRow) string {
	if row.DefaultKey == nil {
		return ""
	}
	return *row.DefaultKey
}

func runIdentify(t *testing.T, db *gorm.DB) {
	t.Helper()
	if err := (IdentifyDefaultCategories20260926{}).Up(context.Background(), db); err != nil {
		t.Fatal(err)
	}
}

func TestIdentifyDefaultCategoriesAdoptsOnlyRecognizableDefaults(t *testing.T) {
	db := identifyFixture(t)
	// Circle 1: the six recognizable defaults, one of them archived, plus a
	// renamed default and a plain custom category. Circle 2: a single default,
	// to prove identity is scoped per circle. Circle 3 has a category with no
	// circle at all, which cannot carry a per-circle identity.
	setup := []string{
		`INSERT INTO labels VALUES (1, 'household', '#26a69a', 1, 7, NULL, true)`,
		`INSERT INTO labels VALUES (2, 'personal', '#7e57c2', 1, 7, NULL, true)`,
		`INSERT INTO labels VALUES (3, 'work', '#616161', 1, 7, NULL, true)`,
		`INSERT INTO labels VALUES (4, 'school', '#80d8ff', 1, 7, NULL, true)`,
		`INSERT INTO labels VALUES (5, 'health', '#4caf50', 1, 7, NULL, true)`,
		`INSERT INTO labels VALUES (6, 'finance', '#ffc107', 1, 7, NULL, true)`,
		`INSERT INTO labels VALUES (7, 'work', '#111111', 2, 8, NULL, true)`,
		`INSERT INTO labels VALUES (8, 'Day job', '#d32f2f', 1, 7, NULL, true)`,
		`INSERT INTO labels VALUES (9, 'Garden', '#26a69a', 1, 7, NULL, true)`,
		`INSERT INTO labels VALUES (10, 'health', '#4caf50', 1, 7, '2026-01-01 00:00:00+00:00', true)`,
		`INSERT INTO labels VALUES (11, 'orphan', '#26a69a', NULL, 7, NULL, true)`,
		`INSERT INTO labels VALUES (12, 'legacy label', '#26a69a', 1, 7, NULL, false)`,
		`INSERT INTO chores VALUES (1, 'chore', 1, 3)`,
	}
	for _, statement := range setup {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}

	runIdentify(t, db)

	rows := readRows(t, db)
	if len(rows) != 12 {
		t.Fatalf("row count changed: %d", len(rows))
	}
	want := map[int]string{
		1: "household", 2: "personal", 3: "work", 4: "school", 5: "health", 6: "finance",
		7: "work",
		// Not identifiable as the default, so they stay unclaimed. Row 10 is an
		// archived duplicate of "health": the lowest id (row 5) wins the
		// identity, so this one remains an ordinary category.
		8:  "", // renamed by the user
		9:  "", // custom category
		10: "", // archived duplicate, out-ranked by row 5
		11: "", // no circle
		12: "", // legacy label, not a category
	}
	for _, row := range rows {
		if got := keyOf(row); got != want[row.ID] {
			t.Fatalf("row %d (%q) identity = %q, want %q", row.ID, row.Name, got, want[row.ID])
		}
	}

	// Nothing but the identity column may change.
	if rows[2].Name != "work" || rows[2].Color != "#616161" || keyOf(rows[2]) != "work" {
		t.Fatalf("the adopted default row was altered: %+v", rows[2])
	}
	if rows[7].Name != "Day job" || rows[7].Color != "#d32f2f" {
		t.Fatalf("a renamed category was overwritten: %+v", rows[7])
	}
	if rows[9].ArchivedAt == nil {
		t.Fatal("an archived default was un-archived")
	}
	if rows[11].Name != "legacy label" || keyOf(rows[11]) != "" {
		t.Fatalf("a legacy label was claimed: %+v", rows[11])
	}
	var chores int64
	db.Table("chores").Count(&chores)
	if chores != 1 {
		t.Fatalf("chore assignments changed: %d", chores)
	}
	var categoryID *int
	if err := db.Table("chores").Select("category_id").Scan(&categoryID).Error; err != nil {
		t.Fatal(err)
	}
	if categoryID == nil || *categoryID != 3 {
		t.Fatalf("the chore no longer points at category 3: %+v", categoryID)
	}
}

func TestIdentifyDefaultCategoriesIsIdempotent(t *testing.T) {
	db := identifyFixture(t)
	if err := db.Exec(`INSERT INTO labels VALUES (1, 'work', '#616161', 1, 7, NULL, true), (2, 'Garden', '', 1, 7, NULL, true)`).Error; err != nil {
		t.Fatal(err)
	}

	runIdentify(t, db)
	afterFirst := readRows(t, db)
	runIdentify(t, db)
	afterSecond := readRows(t, db)

	if len(afterFirst) != len(afterSecond) {
		t.Fatalf("row count changed on re-run: %d -> %d", len(afterFirst), len(afterSecond))
	}
	for i := range afterFirst {
		if afterFirst[i].ID != afterSecond[i].ID ||
			afterFirst[i].Name != afterSecond[i].Name ||
			keyOf(afterFirst[i]) != keyOf(afterSecond[i]) {
			t.Fatalf("re-run changed row %d: %+v -> %+v", afterFirst[i].ID, afterFirst[i], afterSecond[i])
		}
	}
	if keyOf(afterSecond[0]) != "work" {
		t.Fatalf("work identity was lost on re-run: %q", keyOf(afterSecond[0]))
	}
}

func TestIdentifyDefaultCategoriesNeverMovesAnExistingIdentity(t *testing.T) {
	db := identifyFixture(t)
	// An already-identified default that the user renamed. The unclaimed row
	// literally named "work" must not steal that identity.
	setup := []string{
		`ALTER TABLE labels ADD COLUMN default_key TEXT`,
		`INSERT INTO labels (id, name, color, circle_id, created_by, is_category, default_key) VALUES (1, 'Day job', '#d32f2f', 1, 7, true, 'work')`,
		`INSERT INTO labels (id, name, color, circle_id, created_by, is_category, default_key) VALUES (2, 'work', '', 1, 7, true, NULL)`,
	}
	for _, statement := range setup {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}

	runIdentify(t, db)

	rows := readRows(t, db)
	if keyOf(rows[0]) != "work" {
		t.Fatalf("the existing identity was reassigned: %q", keyOf(rows[0]))
	}
	if keyOf(rows[1]) != "" {
		t.Fatalf("a second row was granted the same identity: %q", keyOf(rows[1]))
	}
}

func TestIdentifyDefaultCategoriesAdoptsOneOfDuplicateNames(t *testing.T) {
	db := identifyFixture(t)
	if err := db.Exec(`INSERT INTO labels VALUES (1, 'work', '', 1, 7, NULL, true), (2, 'work', '', 1, 7, NULL, true)`).Error; err != nil {
		t.Fatal(err)
	}

	runIdentify(t, db)

	rows := readRows(t, db)
	// The lowest id wins, so the outcome does not depend on row order.
	if keyOf(rows[0]) != "work" {
		t.Fatalf("the lowest-id duplicate was not adopted: %q", keyOf(rows[0]))
	}
	if keyOf(rows[1]) != "" {
		t.Fatalf("both duplicates were adopted: %q", keyOf(rows[1]))
	}
}

func TestIdentifyDefaultCategoriesScopesIdentityPerCircle(t *testing.T) {
	db := identifyFixture(t)
	setup := []string{
		`INSERT INTO labels VALUES (1, 'work', '', 1, 7, NULL, true)`,
		`INSERT INTO labels VALUES (2, 'work', '', 2, 8, NULL, true)`,
		`INSERT INTO labels VALUES (3, 'work', '', 1, 7, NULL, true)`,
	}
	for _, statement := range setup {
		if err := db.Exec(statement).Error; err != nil {
			t.Fatal(err)
		}
	}

	runIdentify(t, db)

	rows := readRows(t, db)
	// Rows 1 and 2 are in different circles, so both legitimately hold "work".
	if keyOf(rows[0]) != "work" || keyOf(rows[1]) != "work" {
		t.Fatalf("identity was not scoped per circle: %q, %q", keyOf(rows[0]), keyOf(rows[1]))
	}
	// Row 3 duplicates "work" inside circle 1, which already claimed it.
	if keyOf(rows[2]) != "" {
		t.Fatalf("a circle claimed the same identity twice: %q", keyOf(rows[2]))
	}
}

// TestIdentifyDefaultCategoriesAfterModelAutoMigrate covers the real bootstrap
// order. main.go runs database.Migration (which AutoMigrates the models) before
// migrations.Run, so on a live database the default_key column already exists
// and the migration's own ALTER TABLE must be skipped rather than fail.
func TestIdentifyDefaultCategoriesAfterModelAutoMigrate(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&lModel.Label{}); err != nil {
		t.Fatal(err)
	}
	if !hasExactColumn(db, "labels", "default_key") {
		t.Fatal("AutoMigrate did not create default_key; the model tag is wrong")
	}

	circle := 1
	if err := db.Create(&lModel.Label{
		Name: "work", Color: "#616161", CircleID: &circle, CreatedBy: 7, IsCategory: true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// Must not error even though the column already exists.
	runIdentify(t, db)

	rows := readRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("unexpected row count: %d", len(rows))
	}
	if keyOf(rows[0]) != "work" {
		t.Fatalf("AutoMigrated default was not identified: %q", keyOf(rows[0]))
	}
	if rows[0].Name != "work" || rows[0].Color != "#616161" {
		t.Fatalf("the row was altered: %+v", rows[0])
	}
}

// IdentifyDefaultCategoryRowsMatchRuntimeDefaults guards the migration's frozen
// list against the live one. The migration intentionally keeps its own copy so
// that a later change to the defaults cannot retroactively alter what this
// backfill did, but the two must not drift apart unnoticed.
func TestIdentifyDefaultCategoryRowsMatchRuntimeDefaults(t *testing.T) {
	if len(identifyDefaultCategoryRows) != len(lRepo.DefaultCategoryNames) {
		t.Fatalf("frozen list has %d entries, runtime has %d",
			len(identifyDefaultCategoryRows), len(lRepo.DefaultCategoryNames))
	}
	for i, name := range lRepo.DefaultCategoryNames {
		if identifyDefaultCategoryRows[i] != name {
			t.Fatalf("entry %d drifted: migration %q, runtime %q",
				i, identifyDefaultCategoryRows[i], name)
		}
		if _, ok := lRepo.DefaultCategoryColor(name); !ok {
			t.Fatalf("runtime default %q has no seeded color", name)
		}
	}
}
