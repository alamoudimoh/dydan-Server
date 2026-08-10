package reward

import "time"

type RequestStatus string

const (
	RequestStatusPending   RequestStatus = "pending"
	RequestStatusApproved  RequestStatus = "approved"
	RequestStatusRejected  RequestStatus = "rejected"
	RequestStatusCancelled RequestStatus = "cancelled"
)

type Reward struct {
	ID          int       `json:"id" gorm:"primaryKey"`
	CircleID    int       `json:"circleId" gorm:"index;not null"`
	Name        string    `json:"name" gorm:"not null"`
	Description string    `json:"description"`
	CostPoints  int       `json:"costPoints" gorm:"not null;check:cost_points > 0"`
	IsActive    bool      `json:"isActive" gorm:"not null;default:true"`
	CreatedBy   int       `json:"createdBy" gorm:"not null"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

type RewardRequest struct {
	ID                 int           `json:"id" gorm:"primaryKey"`
	CircleID           int           `json:"circleId" gorm:"index;not null"`
	RewardID           int           `json:"rewardId" gorm:"index;not null"`
	UserID             int           `json:"userId" gorm:"index;not null"`
	RewardNameSnapshot string        `json:"rewardName" gorm:"not null"`
	CostPointsSnapshot int           `json:"costPoints" gorm:"not null;check:cost_points_snapshot > 0"`
	Status             RequestStatus `json:"status" gorm:"index;not null"`
	CreatedAt          time.Time     `json:"requestedAt"`
	ResolvedAt         *time.Time    `json:"resolvedAt"`
	ResolvedBy         *int          `json:"resolvedBy"`
	RequesterName      string        `json:"requesterName,omitempty" gorm:"-"`
}

type Balance struct {
	Current   int `json:"currentBalance"`
	Reserved  int `json:"pendingReserved"`
	Spendable int `json:"spendableBalance"`
}
