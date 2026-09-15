package reward

import (
	"context"
	"database/sql"
	"errors"
	"time"

	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	pModel "github.com/alamoudimoh/dydan-server/internal/points"
	"gorm.io/gorm"
)

var (
	ErrNotFound     = errors.New("reward not found")
	ErrInactive     = errors.New("reward is archived")
	ErrInsufficient = errors.New("insufficient spendable points")
	ErrResolved     = errors.New("reward request is already resolved")
)

type Repository struct{ db *gorm.DB }

func NewRepository(db *gorm.DB) *Repository { return &Repository{db: db} }

func (r *Repository) ListRewards(ctx context.Context, circleID int, includeArchived bool) ([]Reward, error) {
	var out []Reward
	q := r.db.WithContext(ctx).Where("circle_id = ?", circleID)
	if !includeArchived {
		q = q.Where("is_active = ?", true)
	}
	err := q.Order("is_active DESC, name ASC").Find(&out).Error
	return out, err
}
func (r *Repository) Create(ctx context.Context, v *Reward) error {
	return r.db.WithContext(ctx).Create(v).Error
}
func (r *Repository) Update(ctx context.Context, circleID, id int, name, description string, cost int, active *bool) (*Reward, error) {
	values := map[string]any{"name": name, "description": description, "cost_points": cost}
	if active != nil {
		values["is_active"] = *active
	}
	res := r.db.WithContext(ctx).Model(&Reward{}).Where("id = ? AND circle_id = ?", id, circleID).Updates(values)
	if res.Error != nil {
		return nil, res.Error
	}
	if res.RowsAffected != 1 {
		return nil, ErrNotFound
	}
	var out Reward
	if err := r.db.WithContext(ctx).Where("id = ? AND circle_id = ?", id, circleID).First(&out).Error; err != nil {
		return nil, err
	}
	return &out, nil
}
func balance(tx *gorm.DB, circleID, userID int) (Balance, error) {
	var m cModel.UserCircle
	if err := tx.Where("circle_id = ? AND user_id = ? AND is_active = ?", circleID, userID, true).First(&m).Error; err != nil {
		return Balance{}, err
	}
	var reserved int64
	if err := tx.Model(&RewardRequest{}).Where("circle_id = ? AND user_id = ? AND status = ?", circleID, userID, RequestStatusPending).Select("COALESCE(SUM(cost_points_snapshot),0)").Scan(&reserved).Error; err != nil {
		return Balance{}, err
	}
	current := m.CurrentBalance()
	spendable := current - int(reserved)
	if spendable < 0 {
		spendable = 0
	}
	return Balance{Current: current, Reserved: int(reserved), Spendable: spendable}, nil
}
func (r *Repository) Balance(ctx context.Context, circleID, userID int) (Balance, error) {
	return balance(r.db.WithContext(ctx), circleID, userID)
}
func (r *Repository) Request(ctx context.Context, circleID, userID, rewardID int) (*RewardRequest, error) {
	var out RewardRequest
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var reward Reward
		if err := tx.Where("id = ? AND circle_id = ?", rewardID, circleID).First(&reward).Error; err != nil {
			return ErrNotFound
		}
		if !reward.IsActive {
			return ErrInactive
		}
		b, err := balance(tx, circleID, userID)
		if err != nil {
			return err
		}
		if b.Spendable < reward.CostPoints {
			return ErrInsufficient
		}
		out = RewardRequest{CircleID: circleID, RewardID: reward.ID, UserID: userID, RewardNameSnapshot: reward.Name, CostPointsSnapshot: reward.CostPoints, Status: RequestStatusPending, CreatedAt: time.Now().UTC()}
		return tx.Create(&out).Error
	}, &sql.TxOptions{Isolation: sql.LevelSerializable})
	return &out, err
}
func (r *Repository) ListRequests(ctx context.Context, circleID int, userID *int, status *RequestStatus) ([]RewardRequest, error) {
	var out []RewardRequest
	q := r.db.WithContext(ctx).Model(&RewardRequest{}).Where("reward_requests.circle_id = ?", circleID)
	if userID != nil {
		q = q.Where("reward_requests.user_id = ?", *userID)
	}
	if status != nil {
		q = q.Where("reward_requests.status = ?", *status)
	}
	err := q.Select("reward_requests.*, COALESCE(NULLIF(users.display_name,''), users.username) AS requester_name").Joins("LEFT JOIN users ON users.id = reward_requests.user_id").Order("reward_requests.created_at DESC, reward_requests.id DESC").Find(&out).Error
	return out, err
}
func (r *Repository) Resolve(ctx context.Context, circleID, requestID, actorID int, status RequestStatus) (*RewardRequest, error) {
	var out RewardRequest
	err := r.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		now := time.Now().UTC()
		res := tx.Model(&RewardRequest{}).Where("id = ? AND circle_id = ? AND status = ?", requestID, circleID, RequestStatusPending).Updates(map[string]any{"status": status, "resolved_at": now, "resolved_by": actorID})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return ErrResolved
		}
		if err := tx.Where("id = ? AND circle_id = ?", requestID, circleID).First(&out).Error; err != nil {
			return err
		}
		if status == RequestStatusApproved {
			res = tx.Model(&cModel.UserCircle{}).Where("user_id = ? AND circle_id = ? AND points - points_redeemed - points_adjusted >= ?", out.UserID, circleID, out.CostPointsSnapshot).Update("points_redeemed", gorm.Expr("points_redeemed + ?", out.CostPointsSnapshot))
			if res.Error != nil {
				return res.Error
			}
			if res.RowsAffected != 1 {
				return ErrInsufficient
			}
			if err := tx.Create(&pModel.PointsHistory{Action: pModel.PointsHistoryActionRedeem, CircleID: circleID, UserID: out.UserID, Points: out.CostPointsSnapshot, CreatedAt: now, CreatedBy: actorID, RewardRequestID: &out.ID}).Error; err != nil {
				return err
			}
		}
		return nil
	})
	return &out, err
}
func (r *Repository) Cancel(ctx context.Context, circleID, requestID, userID int) error {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&RewardRequest{}).Where("id = ? AND circle_id = ? AND user_id = ? AND status = ?", requestID, circleID, userID, RequestStatusPending).Updates(map[string]any{"status": RequestStatusCancelled, "resolved_at": now})
	if res.Error != nil {
		return res.Error
	}
	if res.RowsAffected != 1 {
		return ErrResolved
	}
	return nil
}
