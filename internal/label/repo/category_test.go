package chore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/alamoudimoh/dydan-server/config"
	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	lModel "github.com/alamoudimoh/dydan-server/internal/label/model"
	syncModel "github.com/alamoudimoh/dydan-server/internal/sync/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func categoryRepo(t *testing.T) *LabelRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	// Chores and the sync cursor are migrated alongside labels because a
	// category write bumps the sync version of every chore that embeds it.
	if err := db.AutoMigrate(&lModel.Label{}, &chModel.Chore{}, &syncModel.SyncCursor{}); err != nil {
		t.Fatal(err)
	}
	return NewLabelRepository(db, &config.Config{})
}

// newChore inserts a chore assigned to categoryID in circleID and reports its
// current sync version, so a test can assert the version moved.
func newChore(t *testing.T, repo *LabelRepository, circleID int, categoryID *int) *chModel.Chore {
	t.Helper()
	chore := &chModel.Chore{
		Name:       "chore",
		CircleID:   circleID,
		CategoryID: categoryID,
		IsActive:   true,
	}
	if err := repo.db.Create(chore).Error; err != nil {
		t.Fatal(err)
	}
	return chore
}

func loadChore(t *testing.T, repo *LabelRepository, id int) chModel.Chore {
	t.Helper()
	var chore chModel.Chore
	if err := repo.db.First(&chore, id).Error; err != nil {
		t.Fatal(err)
	}
	return chore
}

func categoryByName(t *testing.T, repo *LabelRepository, circleID int, name string) lModel.Label {
	t.Helper()
	var label lModel.Label
	err := repo.db.Where("circle_id = ? AND is_category = ? AND LOWER(name) = LOWER(?)", circleID, true, name).First(&label).Error
	if err != nil {
		t.Fatalf("category %q not found: %v", name, err)
	}
	return label
}

func TestCategoryCircleScopeDuplicateAndArchive(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleOne, circleTwo := 101, 202
	car := &lModel.Label{Name: "  Car  ", CircleID: &circleOne, CreatedBy: 1}
	if err := repo.CreateCategory(ctx, car); err != nil {
		t.Fatal(err)
	}
	if car.Name != "Car" {
		t.Fatalf("name was not trimmed: %q", car.Name)
	}
	if err := repo.CreateCategory(ctx, &lModel.Label{Name: "car", CircleID: &circleOne, CreatedBy: 2}); !errors.Is(err, ErrDuplicateCategory) {
		t.Fatalf("expected duplicate, got %v", err)
	}
	if err := repo.CreateCategory(ctx, &lModel.Label{Name: "car", CircleID: &circleTwo, CreatedBy: 3}); err != nil {
		t.Fatalf("other Circle should be independent: %v", err)
	}
	if repo.IsActiveCategoryInCircle(ctx, car.ID, circleTwo) {
		t.Fatal("cross-Circle category was assignable")
	}
	if err := repo.UpdateCategory(ctx, circleTwo, &lModel.Label{ID: car.ID, Name: "Forged"}); !errors.Is(err, ErrCategoryNotFound) {
		t.Fatalf("cross-Circle rename: %v", err)
	}
	if err := repo.SetCategoryArchived(ctx, circleTwo, car.ID, true); !errors.Is(err, ErrCategoryNotFound) {
		t.Fatalf("cross-Circle archive: %v", err)
	}
	if err := repo.SetCategoryArchived(ctx, circleOne, car.ID, true); err != nil {
		t.Fatal(err)
	}
	if repo.IsActiveCategoryInCircle(ctx, car.ID, circleOne) {
		t.Fatal("archived category remained assignable")
	}
	all, err := repo.GetCircleCategories(ctx, circleOne, true)
	if err != nil || len(all) != 1 || all[0].ArchivedAt == nil {
		t.Fatalf("archived category not preserved: %+v, %v", all, err)
	}
}

func TestCategoriesNeverIncludeLegacyLabelsOrChoreNames(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleID := 303
	for id, name := range []string{"01_test", "02_test"} {
		if err := repo.db.Create(&lModel.Label{
			ID: id + 1, Name: name, Color: "#26a69a", CircleID: &circleID, CreatedBy: 1,
		}).Error; err != nil {
			t.Fatal(err)
		}
	}
	car := &lModel.Label{Name: "car", CircleID: &circleID, CreatedBy: 1}
	if err := repo.CreateCategory(ctx, car); err != nil {
		t.Fatal(err)
	}

	categories, err := repo.GetCircleCategories(ctx, circleID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(categories) != 1 || categories[0].ID != car.ID || categories[0].Name != "car" {
		t.Fatalf("categories were contaminated by legacy labels: %+v", categories)
	}
}

func TestEnsureDefaultCategoriesBackfillsOnlyItsCircle(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleOne, circleTwo := 404, 505

	// A caller's own category is preserved rather than overwritten or moved.
	custom := &lModel.Label{Name: "Garden", CircleID: &circleOne, CreatedBy: 77}
	if err := repo.CreateCategory(ctx, custom); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureDefaultCategories(ctx, circleOne, 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureDefaultCategories(ctx, circleTwo, 2); err != nil {
		t.Fatal(err)
	}

	one, err := repo.GetCircleCategories(ctx, circleOne, false)
	if err != nil {
		t.Fatal(err)
	}
	two, err := repo.GetCircleCategories(ctx, circleTwo, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(one) != len(DefaultCategoryNames)+1 || len(two) != len(DefaultCategoryNames) {
		t.Fatalf("unexpected defaults: circle one=%d, circle two=%d", len(one), len(two))
	}
	for _, category := range one {
		if category.CircleID == nil || *category.CircleID != circleOne {
			t.Fatalf("circle one received a foreign category: %+v", category)
		}
	}
	for _, category := range two {
		if category.CircleID == nil || *category.CircleID != circleTwo {
			t.Fatalf("circle two received a foreign category: %+v", category)
		}
	}

	// Repeat is idempotent: no duplicate/replay writes.
	if err := repo.EnsureDefaultCategories(ctx, circleOne, 1); err != nil {
		t.Fatal(err)
	}
	again, err := repo.GetCircleCategories(ctx, circleOne, false)
	if err != nil || len(again) != len(one) {
		t.Fatalf("default category replay changed the circle: %d, %v", len(again), err)
	}
}

func TestCreateCategoryPersistsColor(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleID := 606

	car := &lModel.Label{Name: "Garage", Color: "#26a69a", CircleID: &circleID, CreatedBy: 1}
	if err := repo.CreateCategory(ctx, car); err != nil {
		t.Fatal(err)
	}
	stored := categoryByName(t, repo, circleID, "Garage")
	if stored.Color != "#26a69a" {
		t.Fatalf("color was not persisted on create: %q", stored.Color)
	}
}

func TestUpdateCategoryPersistsNameColorAndBoth(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleID := 707
	car := &lModel.Label{Name: "Garden", Color: "#4caf50", CircleID: &circleID, CreatedBy: 1}
	if err := repo.CreateCategory(ctx, car); err != nil {
		t.Fatal(err)
	}

	// Rename only: the color must survive an update that does not mention it.
	if err := repo.UpdateCategory(ctx, circleID, &lModel.Label{ID: car.ID, Name: "Backyard", Color: "#4caf50"}); err != nil {
		t.Fatal(err)
	}
	if stored := categoryByName(t, repo, circleID, "Backyard"); stored.Color != "#4caf50" {
		t.Fatalf("rename dropped the color: %q", stored.Color)
	}

	// Recolor only: the name must survive an update that does not mention it.
	if err := repo.UpdateCategory(ctx, circleID, &lModel.Label{ID: car.ID, Name: "Backyard", Color: "#7e57c2"}); err != nil {
		t.Fatal(err)
	}
	stored := categoryByName(t, repo, circleID, "Backyard")
	if stored.Color != "#7e57c2" {
		t.Fatalf("recolor did not persist: %q", stored.Color)
	}

	// Name and color together.
	if err := repo.UpdateCategory(ctx, circleID, &lModel.Label{ID: car.ID, Name: "Side Yard", Color: "#ffc107"}); err != nil {
		t.Fatal(err)
	}
	stored = categoryByName(t, repo, circleID, "Side Yard")
	if stored.Name != "Side Yard" || stored.Color != "#ffc107" {
		t.Fatalf("combined update did not persist: %+v", stored)
	}
}

func TestArchiveAndRestoreCategoryPreserveAssignments(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleID := 808
	car := &lModel.Label{Name: "Pets", Color: "#80d8ff", CircleID: &circleID, CreatedBy: 1}
	if err := repo.CreateCategory(ctx, car); err != nil {
		t.Fatal(err)
	}
	chore := newChore(t, repo, circleID, &car.ID)

	if err := repo.SetCategoryArchived(ctx, circleID, car.ID, true); err != nil {
		t.Fatal(err)
	}
	// Archived: hidden from new assignment, but the existing assignment and
	// the category row itself are untouched.
	if repo.IsActiveCategoryInCircle(ctx, car.ID, circleID) {
		t.Fatal("archived category was still assignable")
	}
	active, err := repo.GetCircleCategories(ctx, circleID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(active) != 0 {
		t.Fatalf("archived category still offered as active: %+v", active)
	}
	if reloaded := loadChore(t, repo, chore.ID); reloaded.CategoryID == nil || *reloaded.CategoryID != car.ID {
		t.Fatalf("archiving severed the existing assignment: %+v", reloaded.CategoryID)
	}

	// Restore.
	if err := repo.SetCategoryArchived(ctx, circleID, car.ID, false); err != nil {
		t.Fatal(err)
	}
	if !repo.IsActiveCategoryInCircle(ctx, car.ID, circleID) {
		t.Fatal("restored category was not assignable again")
	}
	restored, err := repo.GetCircleCategories(ctx, circleID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(restored) != 1 || restored[0].ID != car.ID || restored[0].ArchivedAt != nil {
		t.Fatalf("restore did not return the category to the active set: %+v", restored)
	}
	// Restoring must not have disturbed the assignment either.
	if reloaded := loadChore(t, repo, chore.ID); reloaded.CategoryID == nil || *reloaded.CategoryID != car.ID {
		t.Fatalf("restore severed the existing assignment: %+v", reloaded.CategoryID)
	}
}

func TestDefaultCategoriesGetColorsAndCustomizedRowsSurvive(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleID := 909

	if err := repo.EnsureDefaultCategories(ctx, circleID, 1); err != nil {
		t.Fatal(err)
	}
	seeded, err := repo.GetCircleCategories(ctx, circleID, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(seeded) != len(DefaultCategoryNames) {
		t.Fatalf("unexpected default count: %d", len(seeded))
	}
	for _, category := range seeded {
		if category.Color == "" {
			t.Fatalf("default %q was seeded without a color", category.Name)
		}
		want, ok := DefaultCategoryColor(category.Name)
		if !ok {
			t.Fatalf("no seeded color for default %q", category.Name)
		}
		if category.Color != want {
			t.Fatalf("default %q seeded color = %q, want %q", category.Name, category.Color, want)
		}
	}

	// A user recolors a default. A later ensure must neither restore the
	// seeded color nor duplicate the row.
	work := categoryByName(t, repo, circleID, "work")
	if err := repo.UpdateCategory(ctx, circleID, &lModel.Label{ID: work.ID, Name: "work", Color: "#d32f2f"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureDefaultCategories(ctx, circleID, 1); err != nil {
		t.Fatal(err)
	}
	customized := categoryByName(t, repo, circleID, "work")
	if customized.Color != "#d32f2f" {
		t.Fatalf("ensure overwrote a customized color: %q", customized.Color)
	}
	if customized.ID != work.ID {
		t.Fatalf("ensure replaced the customized row: %d -> %d", work.ID, customized.ID)
	}
	after, err := repo.GetCircleCategories(ctx, circleID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(after) != len(DefaultCategoryNames) {
		t.Fatalf("ensure reseeded a duplicate default: %d rows", len(after))
	}
	// Seeded colors are stable across repeats, so a circle's palette does not
	// drift between requests.
	for _, category := range after {
		if want, ok := DefaultCategoryColor(category.Name); ok && category.ID != customized.ID && category.Color != want {
			t.Fatalf("default %q color drifted to %q", category.Name, category.Color)
		}
	}
}

func TestEnsureDefaultCategoriesDoesNotResurrectArchivedDefaults(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleID := 1111

	if err := repo.EnsureDefaultCategories(ctx, circleID, 1); err != nil {
		t.Fatal(err)
	}
	health := categoryByName(t, repo, circleID, "health")
	if err := repo.SetCategoryArchived(ctx, circleID, health.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureDefaultCategories(ctx, circleID, 1); err != nil {
		t.Fatal(err)
	}
	all, err := repo.GetCircleCategories(ctx, circleID, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != len(DefaultCategoryNames) {
		t.Fatalf("ensure resurrected the archived default: %d rows", len(all))
	}
	stillArchived := categoryByName(t, repo, circleID, "health")
	if stillArchived.ArchivedAt == nil {
		t.Fatal("ensure un-archived a category the user archived")
	}
}

func TestCategoryWriteBumpsAffectedChoreSyncVersion(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleID := 1010
	car := &lModel.Label{Name: "Travel", Color: "#26a69a", CircleID: &circleID, CreatedBy: 1}
	if err := repo.CreateCategory(ctx, car); err != nil {
		t.Fatal(err)
	}
	// Only this chore embeds the category; the other must not move.
	affected := newChore(t, repo, circleID, &car.ID)
	unaffected := newChore(t, repo, circleID, nil)

	assertBumped := func(stage string, before, after int64) {
		t.Helper()
		if after <= before {
			t.Fatalf("%s: sync version did not advance (%d -> %d)", stage, before, after)
		}
	}

	// Rename.
	before := loadChore(t, repo, affected.ID).SyncVersion
	if err := repo.UpdateCategory(ctx, circleID, &lModel.Label{ID: car.ID, Name: "Trips", Color: "#26a69a"}); err != nil {
		t.Fatal(err)
	}
	renamed := loadChore(t, repo, affected.ID)
	assertBumped("rename", before, renamed.SyncVersion)
	if renamed.SyncVersion == loadChore(t, repo, unaffected.ID).SyncVersion {
		t.Fatal("an unrelated chore was bumped")
	}

	// Recolor.
	before = renamed.SyncVersion
	if err := repo.UpdateCategory(ctx, circleID, &lModel.Label{ID: car.ID, Name: "Trips", Color: "#7e57c2"}); err != nil {
		t.Fatal(err)
	}
	assertBumped("recolor", before, loadChore(t, repo, affected.ID).SyncVersion)

	// Archive, then restore: both change the embedded category.
	before = loadChore(t, repo, affected.ID).SyncVersion
	if err := repo.SetCategoryArchived(ctx, circleID, car.ID, true); err != nil {
		t.Fatal(err)
	}
	archived := loadChore(t, repo, affected.ID)
	assertBumped("archive", before, archived.SyncVersion)

	before = archived.SyncVersion
	if err := repo.SetCategoryArchived(ctx, circleID, car.ID, false); err != nil {
		t.Fatal(err)
	}
	assertBumped("restore", before, loadChore(t, repo, affected.ID).SyncVersion)
}
