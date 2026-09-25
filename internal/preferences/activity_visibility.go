// Package preferences provides runtime-safe access to account preferences for
// consumers outside the HTTP settings handler.
package preferences

import (
	"context"
	"encoding/json"

	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"gorm.io/gorm"
)

// HiddenActivityUserIDs returns only users in the supplied circle that have
// explicitly opted out. It is intentionally a single batch read, allowing
// callers to apply a SQL NOT IN filter rather than issuing preference queries
// per completion row.
func HiddenActivityUserIDs(ctx context.Context, db *gorm.DB, circleID int) ([]int, error) {
	// Older isolated test schemas and pre-preferences deployments retain the
	// historical visible-by-default behavior until the migration is present.
	if !db.Migrator().HasTable(&uModel.UserPreference{}) {
		return nil, nil
	}
	var rows []uModel.UserPreference
	if err := db.WithContext(ctx).Table("user_preferences p").
		Select("p.*").Joins("JOIN users u ON u.id = p.user_id").
		Where("u.circle_id = ?", circleID).Find(&rows).Error; err != nil {
		return nil, err
	}
	hidden := make([]int, 0)
	for _, row := range rows {
		var value struct {
			Privacy struct {
				ActivityVisible *bool `json:"activityVisible"`
			} `json:"privacy"`
		}
		if json.Unmarshal([]byte(row.Value), &value) == nil && value.Privacy.ActivityVisible != nil && !*value.Privacy.ActivityVisible {
			hidden = append(hidden, row.UserID)
		}
	}
	return hidden, nil
}

func ActivityVisible(ctx context.Context, db *gorm.DB, circleID, userID int) (bool, error) {
	hidden, err := HiddenActivityUserIDs(ctx, db, circleID)
	if err != nil {
		return false, err
	}
	for _, id := range hidden {
		if id == userID {
			return false, nil
		}
	}
	return true, nil
}
