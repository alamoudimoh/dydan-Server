package chore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

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

// allCategories returns every category in a circle, archived included, in id
// order, so a test can assert on the complete post-ensure state.
func allCategories(t *testing.T, repo *LabelRepository, circleID int) []lModel.Label {
	t.Helper()
	var labels []lModel.Label
	if err := repo.db.Where("circle_id = ? AND is_category = ?", circleID, true).
		Order("id").Find(&labels).Error; err != nil {
		t.Fatal(err)
	}
	return labels
}

// identityOf reports a category's stable default identity, or "" for a custom
// category.
func identityOf(category lModel.Label) string {
	if category.DefaultKey == nil {
		return ""
	}
	return *category.DefaultKey
}

// countNamesNamed counts the rows in a circle whose name matches, so a test can
// prove a default was not reseeded a second time.
func countNamed(t *testing.T, repo *LabelRepository, circleID int, name string) int {
	t.Helper()
	var count int64
	if err := repo.db.Model(&lModel.Label{}).
		Where("circle_id = ? AND is_category = ? AND LOWER(name) = LOWER(?)", circleID, true, name).
		Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	return int(count)
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

// seedDefaults seeds all six defaults into a circle, mirroring what a first
// category read does for a brand new Circle.
func seedDefaults(t *testing.T, repo *LabelRepository, circleID int) {
	t.Helper()
	if err := repo.EnsureDefaultCategories(context.Background(), circleID, 1); err != nil {
		t.Fatal(err)
	}
}

func TestSeededDefaultsCarryStableIdentity(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleID := 2001
	seedDefaults(t, repo, circleID)

	categories := allCategories(t, repo, circleID)
	if len(categories) != len(DefaultCategoryNames) {
		t.Fatalf("unexpected default count: %d", len(categories))
	}
	for _, category := range categories {
		want := strings.ToLower(category.Name)
		if got := identityOf(category); got != want {
			t.Fatalf("default %q identity = %q, want %q", category.Name, got, want)
		}
	}
	_ = ctx
}

func TestRenamedDefaultIsNotReseededUnderItsOriginalName(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleID := 2002
	seedDefaults(t, repo, circleID)

	work := categoryByName(t, repo, circleID, "work")
	if err := repo.UpdateCategory(ctx, circleID, &lModel.Label{ID: work.ID, Name: "Day job", Color: "#d32f2f"}); err != nil {
		t.Fatal(err)
	}

	// Re-running ensure is what used to resurrect "work" on every read.
	for round := 0; round < 3; round++ {
		if err := repo.EnsureDefaultCategories(ctx, circleID, 1); err != nil {
			t.Fatal(err)
		}
	}

	if count := countNamed(t, repo, circleID, "work"); count != 0 {
		t.Fatalf("renamed default was reseeded under its original name (%d rows)", count)
	}
	categories := allCategories(t, repo, circleID)
	if len(categories) != len(DefaultCategoryNames) {
		t.Fatalf("circle grew to %d rows, want %d", len(categories), len(DefaultCategoryNames))
	}
	// The renamed row kept the identity, its new name and its new color.
	renamed := categoryByName(t, repo, circleID, "Day job")
	if identityOf(renamed) != "work" {
		t.Fatalf("rename lost the default identity: %q", identityOf(renamed))
	}
	if renamed.Color != "#d32f2f" {
		t.Fatalf("rename lost the color: %q", renamed.Color)
	}
	if renamed.ID != work.ID {
		t.Fatalf("rename replaced the row: %d -> %d", work.ID, renamed.ID)
	}
}

func TestRecoloredDefaultIsNotReseeded(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleID := 2003
	seedDefaults(t, repo, circleID)

	health := categoryByName(t, repo, circleID, "health")
	if err := repo.UpdateCategory(ctx, circleID, &lModel.Label{ID: health.ID, Name: "health", Color: "#00bcd4"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureDefaultCategories(ctx, circleID, 1); err != nil {
		t.Fatal(err)
	}

	if count := countNamed(t, repo, circleID, "health"); count != 1 {
		t.Fatalf("recolored default was duplicated: %d rows", count)
	}
	if got := categoryByName(t, repo, circleID, "health"); got.Color != "#00bcd4" || identityOf(got) != "health" {
		t.Fatalf("recolor was reverted or identity lost: %+v", got)
	}
}

func TestArchivedDefaultIsNotReseededAndRestoresWithItsIdentity(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleID := 2004
	seedDefaults(t, repo, circleID)

	school := categoryByName(t, repo, circleID, "school")
	chore := newChore(t, repo, circleID, &school.ID)

	if err := repo.SetCategoryArchived(ctx, circleID, school.ID, true); err != nil {
		t.Fatal(err)
	}
	// Archiving must not cause the default to come back as a fresh row.
	if err := repo.EnsureDefaultCategories(ctx, circleID, 1); err != nil {
		t.Fatal(err)
	}
	if count := countNamed(t, repo, circleID, "school"); count != 1 {
		t.Fatalf("archived default was reseeded as a new row: %d rows", count)
	}
	if all := allCategories(t, repo, circleID); len(all) != len(DefaultCategoryNames) {
		t.Fatalf("archive changed the circle row count: %d", len(all))
	}
	// The existing assignment survives the archive.
	if reloaded := loadChore(t, repo, chore.ID); reloaded.CategoryID == nil || *reloaded.CategoryID != school.ID {
		t.Fatalf("archive severed the existing assignment: %+v", reloaded.CategoryID)
	}

	if err := repo.SetCategoryArchived(ctx, circleID, school.ID, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureDefaultCategories(ctx, circleID, 1); err != nil {
		t.Fatal(err)
	}
	restored := categoryByName(t, repo, circleID, "school")
	if identityOf(restored) != "school" || restored.ID != school.ID {
		t.Fatalf("restore lost the identity or the row: %+v", restored)
	}
	if countNamed(t, repo, circleID, "school") != 1 {
		t.Fatal("restore duplicated the default")
	}
	if all := allCategories(t, repo, circleID); len(all) != len(DefaultCategoryNames) {
		t.Fatalf("restore changed the circle row count: %d", len(all))
	}
}

func TestUserCategorySpelledLikeADefaultIsNotDuplicated(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleID := 2005

	// A category the user made that happens to be spelled like a default. It
	// carries no identity, so identity cannot suppress it, and the name check
	// is what stops a second "work" being seeded next to it.
	custom := &lModel.Label{Name: "Work", CircleID: &circleID, CreatedBy: 1}
	if err := repo.CreateCategory(ctx, custom); err != nil {
		t.Fatal(err)
	}
	if identityOf(*custom) != "" {
		t.Fatalf("a user-created category claimed a default identity: %q", identityOf(*custom))
	}

	if err := repo.EnsureDefaultCategories(ctx, circleID, 1); err != nil {
		t.Fatal(err)
	}

	if count := countNamed(t, repo, circleID, "work"); count != 1 {
		t.Fatalf("a user category named like a default was duplicated: %d rows", count)
	}
	all := allCategories(t, repo, circleID)
	if len(all) != len(DefaultCategoryNames) {
		t.Fatalf("unexpected row count: %d", len(all))
	}
	// The custom row is untouched and the other five defaults are present.
	kept := categoryByName(t, repo, circleID, "Work")
	if kept.ID != custom.ID || identityOf(kept) != "" {
		t.Fatalf("the user category was adopted or replaced: %+v", kept)
	}
	if identityOf(categoryByName(t, repo, circleID, "household")) != "household" {
		t.Fatal("the remaining defaults were not seeded with an identity")
	}
}

func TestCreateCategoryCannotClaimADefaultIdentity(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleID := 2006

	forged := "work"
	car := &lModel.Label{
		Name:       "Forged",
		Color:      "#616161",
		DefaultKey: &forged,
		CircleID:   &circleID,
		CreatedBy:  1,
	}
	if err := repo.CreateCategory(ctx, car); err != nil {
		t.Fatal(err)
	}
	if identityOf(*car) != "" {
		t.Fatalf("CreateCategory let a caller claim the %q identity", identityOf(*car))
	}

	seedDefaults(t, repo, circleID)
	// The real "work" default is still seeded, because the forged row never
	// took the identity.
	if count := countNamed(t, repo, circleID, "work"); count != 1 {
		t.Fatalf("forged identity changed seeding: %d rows named work", count)
	}
	if identityOf(categoryByName(t, repo, circleID, "work")) != "work" {
		t.Fatal("the real work default was not seeded with its identity")
	}
}

func TestPartialBackfillSkipsDefaultsAlreadyClaimedByIdentity(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleID := 2007

	// A circle holding only a renamed "work" plus one custom category: the
	// renamed default keeps its identity, so "work" is not reseeded, while the
	// five genuinely missing defaults are still backfilled.
	renamed := &lModel.Label{Name: "Day job", Color: "#d32f2f", CircleID: &circleID, CreatedBy: 1}
	if err := repo.CreateCategory(ctx, renamed); err != nil {
		t.Fatal(err)
	}
	if err := repo.db.Model(&lModel.Label{}).Where("id = ?", renamed.ID).
		Update("default_key", "work").Error; err != nil {
		t.Fatal(err)
	}
	if err := repo.CreateCategory(ctx, &lModel.Label{Name: "Garden", CircleID: &circleID, CreatedBy: 1}); err != nil {
		t.Fatal(err)
	}

	if err := repo.EnsureDefaultCategories(ctx, circleID, 1); err != nil {
		t.Fatal(err)
	}

	all := allCategories(t, repo, circleID)
	// "Day job" already occupies the "work" slot, so only the five genuinely
	// missing defaults are added: Day job + Garden + 5 = 6 defaults + 1 custom.
	if len(all) != len(DefaultCategoryNames)+1 {
		t.Fatalf("partial backfill produced %d rows, want %d", len(all), len(DefaultCategoryNames)+1)
	}
	if count := countNamed(t, repo, circleID, "work"); count != 0 {
		t.Fatalf("a claimed identity was reseeded under its original name: %d rows", count)
	}
	seeded := 0
	for _, category := range all {
		if identityOf(category) != "" {
			seeded++
		}
	}
	if seeded != len(DefaultCategoryNames) {
		t.Fatalf("expected %d identified defaults, got %d", len(DefaultCategoryNames), seeded)
	}
}

// TestDefaultIdentityIsNotExposedByTheCategoryAPI pins the /api/v1/categories
// response shape. The identity is server-internal bookkeeping, so adding the
// column must not change a single byte of what clients receive.
func TestDefaultIdentityIsNotExposedByTheCategoryAPI(t *testing.T) {
	key := "work"
	archived := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	payload, err := json.Marshal(&lModel.Label{
		ID:         7,
		Name:       "Day job",
		Color:      "#d32f2f",
		CreatedBy:  3,
		ArchivedAt: &archived,
		DefaultKey: &key,
	})
	if err != nil {
		t.Fatal(err)
	}
	var body map[string]any
	if err := json.Unmarshal(payload, &body); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"default_key", "defaultKey", "DefaultKey"} {
		if _, present := body[forbidden]; present {
			t.Fatalf("the default identity leaked into the API payload: %s", payload)
		}
	}
	// The fields clients already rely on are unchanged.
	for _, required := range []string{"id", "name", "color", "created_by", "archivedAt"} {
		if _, present := body[required]; !present {
			t.Fatalf("expected field %q is missing from %s", required, payload)
		}
	}
}

func TestDefaultIdentityIsScopedPerCircle(t *testing.T) {
	repo := categoryRepo(t)
	ctx := context.Background()
	circleOne, circleTwo := 2008, 2009
	seedDefaults(t, repo, circleOne)
	seedDefaults(t, repo, circleTwo)

	// Renaming the default in one circle must not affect the other.
	work := categoryByName(t, repo, circleOne, "work")
	if err := repo.UpdateCategory(ctx, circleOne, &lModel.Label{ID: work.ID, Name: "Day job", Color: "#d32f2f"}); err != nil {
		t.Fatal(err)
	}
	if err := repo.EnsureDefaultCategories(ctx, circleOne, 1); err != nil {
		t.Fatal(err)
	}

	if count := countNamed(t, repo, circleOne, "work"); count != 0 {
		t.Fatalf("circle one was reseeded: %d rows named work", count)
	}
	other := categoryByName(t, repo, circleTwo, "work")
	if identityOf(other) != "work" {
		t.Fatalf("circle two's default lost its identity: %q", identityOf(other))
	}
	if count := countNamed(t, repo, circleTwo, "work"); count != 1 {
		t.Fatalf("circle two was affected by circle one's rename: %d rows", count)
	}
}
