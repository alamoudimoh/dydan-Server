package migrations

import (
	"context"

	cModel "donetick.com/core/internal/circle/model"
	pModel "donetick.com/core/internal/points"
	"gorm.io/gorm"
)

type SeparateResetAdjustments20260810 struct{}

func (m SeparateResetAdjustments20260810) ID() string {
	// This repair only depends on AutoMigrate-created columns and must run
	// before legacy data migrations, some of which may be inapplicable to a
	// fresh schema and stop the remaining registry for that startup.
	return "20240101_separate_reset_adjustments"
}

func (m SeparateResetAdjustments20260810) Description() string {
	return "Move historical reset adjustments out of redeemed points while preserving balances and audit history."
}

func (m SeparateResetAdjustments20260810) Up(ctx context.Context, db *gorm.DB) error {
	type resetTotal struct {
		CircleID int `gorm:"column:circle_id"`
		UserID   int `gorm:"column:user_id"`
		Points   int `gorm:"column:points"`
	}

	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		var totals []resetTotal
		if err := tx.Model(&pModel.PointsHistory{}).
			Select("circle_id, user_id, COALESCE(SUM(previous_balance), 0) AS points").
			Where("action = ?", pModel.PointsHistoryActionReset).
			Group("circle_id, user_id").
			Scan(&totals).Error; err != nil {
			return err
		}
		for _, total := range totals {
			if err := tx.Model(&cModel.UserCircle{}).
				Where("circle_id = ? AND user_id = ?", total.CircleID, total.UserID).
				Updates(map[string]interface{}{
					"points_adjusted": total.Points,
					"points_redeemed": gorm.Expr("points_redeemed - ?", total.Points),
				}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (m SeparateResetAdjustments20260810) Down(ctx context.Context, db *gorm.DB) error {
	return nil
}

func init() {
	Register(SeparateResetAdjustments20260810{})
}
