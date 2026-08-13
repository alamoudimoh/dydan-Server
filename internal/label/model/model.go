package model

import "time"

type Label struct {
	ID         int        `json:"id" gorm:"primary_key"`
	Name       string     `json:"name" gorm:"column:name"`
	Color      string     `json:"color" gorm:"column:color"`
	CircleID   *int       `json:"-" gorm:"column:circle_id"`
	CreatedBy  int        `json:"created_by" gorm:"column:created_by"`
	ArchivedAt *time.Time `json:"archivedAt,omitempty" gorm:"column:archived_at;index"`
	IsCategory bool       `json:"-" gorm:"column:is_category;not null;default:false;index"`
}

type LabelReq struct {
	LabelID int `json:"id" binding:"required,gt=0"`
}
