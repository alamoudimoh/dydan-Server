package user

import (
	"context"
	nModel "donetick.com/core/internal/notifier/model"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"time"
)

type InboxRepository struct{ db *gorm.DB }

func NewInboxRepository(db *gorm.DB) *InboxRepository { return &InboxRepository{db: db} }

func (r *InboxRepository) Create(ctx context.Context, n *nModel.InboxNotification) (bool, error) {
	if n.CreatedAt.IsZero() {
		n.CreatedAt = time.Now().UTC()
	}
	res := r.db.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "dedup_key"}}, DoNothing: true}).Create(n)
	return res.RowsAffected == 1, res.Error
}
func (r *InboxRepository) List(ctx context.Context, uid, cid, limit, before int) ([]nModel.InboxNotification, error) {
	if limit < 1 || limit > 100 {
		limit = 30
	}
	var out []nModel.InboxNotification
	q := r.db.WithContext(ctx).Where("user_id = ? AND circle_id = ?", uid, cid)
	if before > 0 {
		q = q.Where("id < ?", before)
	}
	err := q.Order("created_at DESC, id DESC").Limit(limit).Find(&out).Error
	return out, err
}
func (r *InboxRepository) UnreadCount(ctx context.Context, uid, cid int) (int64, error) {
	var count int64
	err := r.db.WithContext(ctx).Model(&nModel.InboxNotification{}).Where("user_id = ? AND circle_id = ? AND read_at IS NULL", uid, cid).Count(&count).Error
	return count, err
}
func (r *InboxRepository) MarkRead(ctx context.Context, id, uid, cid int) (bool, error) {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&nModel.InboxNotification{}).Where("id = ? AND user_id = ? AND circle_id = ? AND read_at IS NULL", id, uid, cid).Update("read_at", now)
	if res.Error != nil || res.RowsAffected == 1 {
		return res.RowsAffected == 1, res.Error
	}
	var count int64
	err := r.db.WithContext(ctx).Model(&nModel.InboxNotification{}).Where("id = ? AND user_id = ? AND circle_id = ?", id, uid, cid).Count(&count).Error
	return count == 1, err
}
func (r *InboxRepository) MarkAllRead(ctx context.Context, uid, cid int) (int64, error) {
	now := time.Now().UTC()
	res := r.db.WithContext(ctx).Model(&nModel.InboxNotification{}).Where("user_id = ? AND circle_id = ? AND read_at IS NULL", uid, cid).Update("read_at", now)
	return res.RowsAffected, res.Error
}
