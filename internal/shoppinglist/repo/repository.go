package repo

import (
	"context"
	"errors"
	"strings"
	"time"

	slModel "donetick.com/core/internal/shoppinglist/model"
	"gorm.io/gorm"
)

var (
	ErrListNotFound  = errors.New("shopping list not found")
	ErrItemNotFound  = errors.New("shopping list item not found")
	ErrForbidden     = errors.New("permission denied")
	ErrInvalidScope  = errors.New("invalid list scope")
	ErrArchived      = errors.New("shopping list is archived")
	ErrInvalidName   = errors.New("invalid list name")
	ErrInvalidItem   = errors.New("invalid item name")
)

type Repository struct {
	db *gorm.DB
}

func NewRepository(db *gorm.DB) *Repository {
	return &Repository{db: db}
}

func isPersonal(list *slModel.ShoppingList) bool {
	return list.CircleID == nil
}

func canAccessList(userID, circleID int, list *slModel.ShoppingList, circleMember bool) bool {
	if isPersonal(list) {
		return list.CreatedBy == userID
	}
	return circleMember && list.CircleID != nil && *list.CircleID == circleID
}

func canManageList(userID int, list *slModel.ShoppingList, circleAdmin bool) bool {
	if isPersonal(list) {
		return list.CreatedBy == userID
	}
	return list.CreatedBy == userID || circleAdmin
}

func canEditItems(userID, circleID int, list *slModel.ShoppingList, circleMember bool) bool {
	return canAccessList(userID, circleID, list, circleMember)
}

func (r *Repository) ListAccessible(ctx context.Context, userID, circleID int, circleMember bool, includeArchived bool) ([]slModel.ShoppingListSummary, error) {
	query := r.db.WithContext(ctx).Model(&slModel.ShoppingList{})
	if includeArchived {
		query = query.Unscoped()
	} else {
		query = query.Where("archived_at IS NULL")
	}

	if circleMember && circleID > 0 {
		query = query.Where(
			"(circle_id IS NULL AND created_by = ?) OR (circle_id = ?)",
			userID, circleID,
		)
	} else {
		query = query.Where("circle_id IS NULL AND created_by = ?", userID)
	}

	var lists []slModel.ShoppingList
	if err := query.Order("name ASC").Find(&lists).Error; err != nil {
		return nil, err
	}

	summaries := make([]slModel.ShoppingListSummary, 0, len(lists))
	for _, list := range lists {
		itemCount, checkedCount, err := r.countItems(ctx, list.ID)
		if err != nil {
			return nil, err
		}
		summaries = append(summaries, slModel.ShoppingListSummary{
			ShoppingList: list,
			ItemCount:    itemCount,
			CheckedCount: checkedCount,
		})
	}
	return summaries, nil
}

func (r *Repository) countItems(ctx context.Context, listID int) (int, int, error) {
	var itemCount int64
	var checkedCount int64
	if err := r.db.WithContext(ctx).Model(&slModel.ShoppingListItem{}).Where("list_id = ?", listID).Count(&itemCount).Error; err != nil {
		return 0, 0, err
	}
	if err := r.db.WithContext(ctx).Model(&slModel.ShoppingListItem{}).Where("list_id = ? AND is_checked = ?", listID, true).Count(&checkedCount).Error; err != nil {
		return 0, 0, err
	}
	return int(itemCount), int(checkedCount), nil
}

func (r *Repository) GetByID(ctx context.Context, listID int) (*slModel.ShoppingList, error) {
	var list slModel.ShoppingList
	if err := r.db.WithContext(ctx).Where("id = ?", listID).First(&list).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrListNotFound
		}
		return nil, err
	}
	return &list, nil
}

func (r *Repository) GetDetail(ctx context.Context, listID int) (*slModel.ShoppingListDetail, error) {
	list, err := r.GetByID(ctx, listID)
	if err != nil {
		return nil, err
	}
	var items []slModel.ShoppingListItem
	if err := r.db.WithContext(ctx).Where("list_id = ?", listID).Order("is_checked ASC, sort_order ASC, id ASC").Find(&items).Error; err != nil {
		return nil, err
	}
	return &slModel.ShoppingListDetail{ShoppingList: *list, Items: items}, nil
}

func (r *Repository) CreateList(ctx context.Context, userID, circleID int, circleMember bool, req slModel.CreateListReq) (*slModel.ShoppingList, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 120 {
		return nil, ErrInvalidName
	}

	list := &slModel.ShoppingList{Name: name, CreatedBy: userID}
	switch req.Scope {
	case slModel.ScopePersonal:
		list.CircleID = nil
	case slModel.ScopeCircle:
		if !circleMember || circleID < 1 {
			return nil, ErrForbidden
		}
		list.CircleID = &circleID
	default:
		return nil, ErrInvalidScope
	}

	if err := r.db.WithContext(ctx).Create(list).Error; err != nil {
		return nil, err
	}
	return list, nil
}

func (r *Repository) RenameList(ctx context.Context, list *slModel.ShoppingList, name string) error {
	name = strings.TrimSpace(name)
	if name == "" || len([]rune(name)) > 120 {
		return ErrInvalidName
	}
	return r.db.WithContext(ctx).Model(list).Update("name", name).Error
}

func (r *Repository) ArchiveList(ctx context.Context, listID int) error {
	now := time.Now().UTC()
	return r.db.WithContext(ctx).Model(&slModel.ShoppingList{}).Where("id = ?", listID).Update("archived_at", now).Error
}

func (r *Repository) DeleteList(ctx context.Context, listID int) error {
	return r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("list_id = ?", listID).Delete(&slModel.ShoppingListItem{}).Error; err != nil {
			return err
		}
		result := tx.Where("id = ?", listID).Delete(&slModel.ShoppingList{})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return ErrListNotFound
		}
		return nil
	})
}

func (r *Repository) CreateItem(ctx context.Context, listID, userID int, req slModel.CreateItemReq) (*slModel.ShoppingListItem, error) {
	name := strings.TrimSpace(req.Name)
	if name == "" || len([]rune(name)) > 200 {
		return nil, ErrInvalidItem
	}
	var maxOrder int
	r.db.WithContext(ctx).Model(&slModel.ShoppingListItem{}).Where("list_id = ?", listID).Select("COALESCE(MAX(sort_order), 0)").Scan(&maxOrder)

	item := &slModel.ShoppingListItem{
		ListID:    listID,
		Name:      name,
		Quantity:  req.Quantity,
		SortOrder: maxOrder + 1,
		CreatedBy: userID,
	}
	if err := r.db.WithContext(ctx).Create(item).Error; err != nil {
		return nil, err
	}
	return item, nil
}

func (r *Repository) UpdateItem(ctx context.Context, itemID, listID int, userID int, req slModel.UpdateItemReq) (*slModel.ShoppingListItem, error) {
	var item slModel.ShoppingListItem
	if err := r.db.WithContext(ctx).Where("id = ? AND list_id = ?", itemID, listID).First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrItemNotFound
		}
		return nil, err
	}

	updates := map[string]interface{}{}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" || len([]rune(name)) > 200 {
			return nil, ErrInvalidItem
		}
		updates["name"] = name
	}
	if req.Quantity != nil {
		updates["quantity"] = req.Quantity
	}
	if req.SortOrder != nil {
		updates["sort_order"] = *req.SortOrder
	}
	if req.IsChecked != nil {
		updates["is_checked"] = *req.IsChecked
		if *req.IsChecked {
			now := time.Now().UTC()
			updates["checked_at"] = now
			updates["checked_by"] = userID
		} else {
			updates["checked_at"] = nil
			updates["checked_by"] = nil
		}
	}

	if len(updates) > 0 {
		if err := r.db.WithContext(ctx).Model(&item).Updates(updates).Error; err != nil {
			return nil, err
		}
	}

	if err := r.db.WithContext(ctx).Where("id = ?", itemID).First(&item).Error; err != nil {
		return nil, err
	}
	return &item, nil
}

func (r *Repository) DeleteItem(ctx context.Context, itemID, listID int) error {
	result := r.db.WithContext(ctx).Where("id = ? AND list_id = ?", itemID, listID).Delete(&slModel.ShoppingListItem{})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected == 0 {
		return ErrItemNotFound
	}
	return nil
}

func (r *Repository) ClearCompleted(ctx context.Context, listID int) (int64, error) {
	result := r.db.WithContext(ctx).Where("list_id = ? AND is_checked = ?", listID, true).Delete(&slModel.ShoppingListItem{})
	return result.RowsAffected, result.Error
}

func CanAccessList(userID, circleID int, list *slModel.ShoppingList, circleMember bool) bool {
	return canAccessList(userID, circleID, list, circleMember)
}

func CanManageList(userID int, list *slModel.ShoppingList, circleAdmin bool) bool {
	return canManageList(userID, list, circleAdmin)
}

func CanEditItems(userID, circleID int, list *slModel.ShoppingList, circleMember bool) bool {
	return canEditItems(userID, circleID, list, circleMember)
}
