package repo_test

import (
	"context"
	"testing"

	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	slModel "github.com/alamoudimoh/daydan-server/internal/shoppinglist/model"
	slRepo "github.com/alamoudimoh/daydan-server/internal/shoppinglist/repo"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTestRepo(t *testing.T) *slRepo.Repository {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := db.AutoMigrate(&slModel.ShoppingList{}, &slModel.ShoppingListItem{}, &cModel.UserCircle{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return slRepo.NewRepository(db)
}

func TestPersonalListLifecycle(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	list, err := repo.CreateList(ctx, 10, 0, false, slModel.CreateListReq{Name: "Groceries", Scope: slModel.ScopePersonal})
	if err != nil {
		t.Fatalf("create list: %v", err)
	}
	if list.CircleID != nil {
		t.Fatalf("expected personal list")
	}

	item, err := repo.CreateItem(ctx, list.ID, 10, slModel.CreateItemReq{Name: "Milk"})
	if err != nil {
		t.Fatalf("create item: %v", err)
	}
	checked := true
	updated, err := repo.UpdateItem(ctx, item.ID, list.ID, 10, slModel.UpdateItemReq{IsChecked: &checked})
	if err != nil || !updated.IsChecked {
		t.Fatalf("check item: %v %+v", err, updated)
	}

	removed, err := repo.ClearCompleted(ctx, list.ID)
	if err != nil || removed != 1 {
		t.Fatalf("clear completed: removed=%d err=%v", removed, err)
	}

	if err := repo.ArchiveList(ctx, list.ID); err != nil {
		t.Fatalf("archive: %v", err)
	}
	lists, err := repo.ListAccessible(ctx, 10, 0, false, false)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(lists) != 0 {
		t.Fatalf("expected archived list hidden, got %d", len(lists))
	}
}

func TestCircleListRequiresMembership(t *testing.T) {
	repo := newTestRepo(t)
	ctx := context.Background()

	_, err := repo.CreateList(ctx, 10, 5, false, slModel.CreateListReq{Name: "Family", Scope: slModel.ScopeCircle})
	if err == nil {
		t.Fatal("expected forbidden without circle membership")
	}

	list, err := repo.CreateList(ctx, 10, 5, true, slModel.CreateListReq{Name: "Family", Scope: slModel.ScopeCircle})
	if err != nil {
		t.Fatalf("create circle list: %v", err)
	}
	if list.CircleID == nil || *list.CircleID != 5 {
		t.Fatalf("expected circle id 5, got %+v", list.CircleID)
	}

	lists, err := repo.ListAccessible(ctx, 10, 5, true, false)
	if err != nil || len(lists) != 1 {
		t.Fatalf("list accessible: %v len=%d", err, len(lists))
	}
}

func TestPermissions(t *testing.T) {
	personal := &slModel.ShoppingList{CreatedBy: 1}
	circleID := 9
	circle := &slModel.ShoppingList{CreatedBy: 2, CircleID: &circleID}

	if !slRepo.CanAccessList(1, 0, personal, false) {
		t.Fatal("owner should access personal list")
	}
	if slRepo.CanAccessList(2, 0, personal, false) {
		t.Fatal("other user should not access personal list")
	}
	if !slRepo.CanAccessList(3, 9, circle, true) {
		t.Fatal("circle member should access circle list")
	}
}

func TestManageListRequiresListCircleMembership(t *testing.T) {
	personal := &slModel.ShoppingList{CreatedBy: 1}
	circleID := 9
		circle := &slModel.ShoppingList{CreatedBy: 2, CircleID: &circleID}

	if !slRepo.CanManageList(1, 9, personal, false, false) {
		t.Fatal("owner should manage own personal list")
	}
	if !slRepo.CanManageList(2, 9, circle, true, false) {
		t.Fatal("creator should manage circle list while a member")
	}
	if !slRepo.CanManageList(99, 9, circle, true, true) {
		t.Fatal("circle admin should manage circle list")
	}
	// Admin of another circle must not manage this circle's list.
	if slRepo.CanManageList(99, 10, circle, true, true) {
		t.Fatal("admin of a different circle must not manage the list")
	}
	// Creator who left the circle loses management rights.
	if slRepo.CanManageList(2, 9, circle, false, false) {
		t.Fatal("non-member creator must not manage the circle list")
	}
}
