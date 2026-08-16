package model

import (
	"time"

	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
)

type UserExtended struct {
	uModel.User
	Credit             int        `gorm:"column:amount;->"`
	SubscriptionStatus *string    `gorm:"column:status;<-:false"`     // read one column
	ExpiredAt          *time.Time `gorm:"column:expired_at;<-:false"` // read one column
}
