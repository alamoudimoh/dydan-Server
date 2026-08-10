package routine

import (
	"time"

	chModel "donetick.com/core/internal/chore/model"
)

type Pack struct {
	ID          int       `json:"id" gorm:"primaryKey"`
	CircleID    int       `json:"circleId" gorm:"column:circle_id;not null;index"`
	Name        string    `json:"name" gorm:"column:name;not null"`
	Description string    `json:"description" gorm:"column:description;type:text"`
	IsActive    bool      `json:"isActive" gorm:"column:is_active;not null;default:true;index"`
	CreatedBy   int       `json:"createdBy" gorm:"column:created_by;not null"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
	Items       []Item    `json:"items" gorm:"foreignKey:PackID;constraint:OnDelete:CASCADE"`
}

func (Pack) TableName() string { return "routine_packs" }

type Item struct {
	ID                   int                           `json:"id" gorm:"primaryKey"`
	PackID               int                           `json:"packId" gorm:"column:pack_id;not null;index"`
	Position             int                           `json:"position" gorm:"column:position;not null"`
	Name                 string                        `json:"name" gorm:"column:name;not null"`
	Description          *string                       `json:"description,omitempty" gorm:"type:text"`
	FrequencyType        chModel.FrequencyType         `json:"frequencyType" gorm:"column:frequency_type;not null"`
	Frequency            int                           `json:"frequency" gorm:"column:frequency;not null;default:1"`
	FrequencyMetadata    *chModel.FrequencyMetadata    `json:"frequencyMetadata,omitempty" gorm:"column:frequency_metadata;serializer:json;type:text"`
	IsRolling            bool                          `json:"isRolling"`
	AssigneeIDs          []int                         `json:"assigneeIds" gorm:"column:assignee_ids;serializer:json;type:text"`
	AssignedTo           *int                          `json:"assignedTo,omitempty"`
	AssignStrategy       chModel.AssignmentStrategy    `json:"assignStrategy" gorm:"column:assign_strategy;not null"`
	Points               *int                          `json:"points,omitempty"`
	RequireApproval      bool                          `json:"requireApproval"`
	IsPrivate            bool                          `json:"isPrivate"`
	Priority             int                           `json:"priority"`
	CompletionWindow     *int                          `json:"completionWindow,omitempty"`
	LabelIDs             []int                         `json:"labelIds" gorm:"column:label_ids;serializer:json;type:text"`
	SubTaskNames         []string                      `json:"subTaskNames" gorm:"column:subtask_names;serializer:json;type:text"`
	ProjectID            *int                          `json:"projectId,omitempty"`
	AssetID              *int                          `json:"assetId,omitempty"`
	RequireAsset         bool                          `json:"requireAsset"`
	MeterID              *int                          `json:"meterId,omitempty"`
	MeterInterval        *int64                        `json:"meterInterval,omitempty"`
	Notification         bool                          `json:"notification"`
	NotificationMetadata *chModel.NotificationMetadata `json:"notificationMetadata,omitempty" gorm:"column:notification_metadata;serializer:json;type:text"`
	CreatedAt            time.Time                     `json:"createdAt"`
	UpdatedAt            time.Time                     `json:"updatedAt"`
}

func (Item) TableName() string { return "routine_items" }

type Application struct {
	ID             int       `json:"id" gorm:"primaryKey"`
	CircleID       int       `json:"circleId" gorm:"column:circle_id;not null;uniqueIndex:idx_routine_apply_key"`
	PackID         int       `json:"packId" gorm:"column:pack_id;not null"`
	IdempotencyKey string    `json:"-" gorm:"column:idempotency_key;not null;uniqueIndex:idx_routine_apply_key"`
	ChoreIDs       []int     `json:"choreIds" gorm:"column:chore_ids;serializer:json;type:text"`
	AppliedBy      int       `json:"appliedBy" gorm:"column:applied_by;not null"`
	AppliedAt      time.Time `json:"appliedAt"`
}

func (Application) TableName() string { return "routine_applications" }
