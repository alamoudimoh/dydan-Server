package chore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"donetick.com/core/config"
	lModel "donetick.com/core/internal/label/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func categoryRepo(t *testing.T) *LabelRepository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(fmt.Sprintf("file:%s?mode=memory&cache=shared", t.Name())), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&lModel.Label{}); err != nil {
		t.Fatal(err)
	}
	return NewLabelRepository(db, &config.Config{})
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
