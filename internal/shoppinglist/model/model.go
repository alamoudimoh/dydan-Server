package model

import "time"

const (
	ScopePersonal = "personal"
	ScopeCircle   = "circle"
)

type ShoppingList struct {
	ID         int        `json:"id" gorm:"primary_key"`
	Name       string     `json:"name" gorm:"column:name;not null"`
	CircleID   *int       `json:"circleId,omitempty" gorm:"column:circle_id;index"`
	CreatedBy  int        `json:"createdBy" gorm:"column:created_by;not null;index"`
	ArchivedAt *time.Time `json:"archivedAt,omitempty" gorm:"column:archived_at;index"`
	CreatedAt  time.Time  `json:"createdAt" gorm:"column:created_at;autoCreateTime"`
	UpdatedAt  time.Time  `json:"updatedAt" gorm:"column:updated_at;autoUpdateTime"`
}

func (ShoppingList) TableName() string { return "shopping_lists" }

type ShoppingListItem struct {
	ID        int        `json:"id" gorm:"primary_key"`
	ListID    int        `json:"listId" gorm:"column:list_id;not null;index"`
	Name      string     `json:"name" gorm:"column:name;not null"`
	Quantity  *string    `json:"quantity,omitempty" gorm:"column:quantity"`
	IsChecked bool       `json:"isChecked" gorm:"column:is_checked;default:false"`
	SortOrder int        `json:"sortOrder" gorm:"column:sort_order;default:0"`
	CreatedBy int        `json:"createdBy" gorm:"column:created_by;not null"`
	CheckedBy *int       `json:"checkedBy,omitempty" gorm:"column:checked_by"`
	CheckedAt *time.Time `json:"checkedAt,omitempty" gorm:"column:checked_at"`
	CreatedAt time.Time  `json:"createdAt" gorm:"column:created_at;autoCreateTime"`
	UpdatedAt time.Time  `json:"updatedAt" gorm:"column:updated_at;autoUpdateTime"`
}

func (ShoppingListItem) TableName() string { return "shopping_list_items" }

type ShoppingListSummary struct {
	ShoppingList
	ItemCount    int `json:"itemCount"`
	CheckedCount int `json:"checkedCount"`
}

type ShoppingListDetail struct {
	ShoppingList
	Items []ShoppingListItem `json:"items"`
}

type CreateListReq struct {
	Name  string `json:"name" binding:"required,min=1,max=120"`
	Scope string `json:"scope" binding:"required,oneof=personal circle"`
}

type UpdateListReq struct {
	Name string `json:"name" binding:"required,min=1,max=120"`
}

type CreateItemReq struct {
	Name     string  `json:"name" binding:"required,min=1,max=200"`
	Quantity *string `json:"quantity" binding:"omitempty,max=40"`
}

type UpdateItemReq struct {
	Name      *string `json:"name" binding:"omitempty,min=1,max=200"`
	Quantity  *string `json:"quantity" binding:"omitempty,max=40"`
	IsChecked *bool   `json:"isChecked"`
	SortOrder *int    `json:"sortOrder"`
}
