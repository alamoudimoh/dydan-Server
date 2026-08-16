package model

import (
	lModel "github.com/alamoudimoh/daydan-server/internal/label/model"
	"time"
)

type Thing struct {
	ID          int           `json:"id" gorm:"primary_key"`
	UserID      int           `json:"userID" gorm:"column:user_id"`
	CircleID    int           `json:"circleId" gorm:"column:circle_id"`
	Name        string        `json:"name" gorm:"column:name"`
	Description *string       `json:"description,omitempty" gorm:"column:description;type:text"`
	Category    string        `json:"category,omitempty" gorm:"column:category"`
	CategoryID  *int          `json:"categoryId,omitempty" gorm:"column:category_id;index"`
	CategoryRef *lModel.Label `json:"categoryRef,omitempty" gorm:"foreignKey:CategoryID;references:ID"`
	State       string        `json:"state" gorm:"column:state"`
	Type        string        `json:"type" gorm:"column:type"`
	ArchivedAt  *time.Time    `json:"archivedAt,omitempty" gorm:"column:archived_at;index"`
	ThingChores []ThingChore  `json:"thingChores" gorm:"foreignkey:ThingID;references:ID"`
	UpdatedAt   *time.Time    `json:"updatedAt" gorm:"column:updated_at"`
	CreatedAt   *time.Time    `json:"createdAt" gorm:"column:created_at"`
}

type ThingHistory struct {
	ID        int        `json:"id" gorm:"primary_key"`
	ThingID   int        `json:"thingId" gorm:"column:thing_id"`
	State     string     `json:"state" gorm:"column:state"`
	UpdatedAt *time.Time `json:"updatedAt" gorm:"column:updated_at"`
	CreatedAt *time.Time `json:"createdAt" gorm:"column:created_at"`
}

type ThingChore struct {
	ThingID      int    `json:"thingId" gorm:"column:thing_id;primaryKey;uniqueIndex:idx_thing_user"`
	ChoreID      int    `json:"choreId" gorm:"column:chore_id;primaryKey;uniqueIndex:idx_thing_user"`
	TriggerState string `json:"triggerState" gorm:"column:trigger_state"`
	Condition    string `json:"condition" gorm:"column:condition"`
}

type ThingTrigger struct {
	ID           int    `json:"thingID" binding:"required"`
	TriggerState string `json:"triggerState" binding:"required"`
	Condition    string `json:"condition"`
}

type ThingType string

const (
	ThingTypeText    ThingType = "text"
	ThingTypeNumber  ThingType = "number"
	ThingTypeBoolean ThingType = "boolean"
	ThingTypeAction  ThingType = "action"
)
