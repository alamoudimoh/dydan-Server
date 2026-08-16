package repo

import (
	"context"
	"errors"
	"time"

	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	pModel "github.com/alamoudimoh/daydan-server/internal/points"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/alamoudimoh/daydan-server/logging"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var (
	ErrInsufficientPoints = errors.New("insufficient available points")
	ErrLastAdmin          = errors.New("circle must retain at least one active admin")
)

type ICircleRepository interface {
	CreateCircle(circle *cModel.Circle) error
	AddUserToCircle(circleUser *cModel.UserCircle) error
	GetCircleUsers(circleID int) ([]*cModel.UserCircle, error)
	GetUserCircles(userID int) ([]*cModel.Circle, error)
	DeleteUserFromCircle(circleID, userID int) error
	ChangeUserRole(circleID, userID int, role string) error
	GetCircleByInviteCode(inviteCode string) (*cModel.Circle, error)
	GetCircleByID(circleID int) (*cModel.Circle, error)
}

type CircleRepository struct {
	db *gorm.DB
}

func NewCircleRepository(db *gorm.DB) *CircleRepository {
	return &CircleRepository{db}
}

func (r *CircleRepository) CreateCircle(c context.Context, circle *cModel.Circle) (*cModel.Circle, error) {
	if err := r.db.WithContext(c).Save(&circle).Error; err != nil {
		return nil, err
	}
	return circle, nil

}

func (r *CircleRepository) AddUserToCircle(c context.Context, circleUser *cModel.UserCircle) error {
	// Use FirstOrCreate to handle the unique constraint
	// This will create the record if it doesn't exist, or return the existing one
	result := r.db.WithContext(c).Where(cModel.UserCircle{
		UserID:   circleUser.UserID,
		CircleID: circleUser.CircleID,
	}).FirstOrCreate(circleUser)

	return result.Error
}

func (r *CircleRepository) GetCircleUsers(c context.Context, circleID int) ([]*cModel.UserCircleDetail, error) {
	var circleUsers []*cModel.UserCircleDetail
	if err := r.db.WithContext(c).
		Table("user_circles uc").
		Select("uc.*, uc.points - uc.points_redeemed - uc.points_adjusted AS points_available, u.username, u.display_name, u.chat_id, u.image, unt.user_id as user_id, unt.target_id as target_id, unt.type as notification_type").
		Joins("left join users u on u.id = uc.user_id").
		Joins("left join user_notification_targets unt on unt.user_id = u.id").
		Where("uc.circle_id = ?", circleID).
		Scan(&circleUsers).Error; err != nil {
		return nil, err
	}
	return circleUsers, nil
}

func (r *CircleRepository) GetPendingJoinRequests(c context.Context, circleID int) ([]*cModel.UserCircleDetail, error) {
	var pendingRequests []*cModel.UserCircleDetail
	if err := r.db.WithContext(c).Raw("SELECT *, user_circles.id as id FROM user_circles LEFT JOIN users on users.id = user_circles.user_id WHERE user_circles.circle_id = ? AND user_circles.is_active = false", circleID).Scan(&pendingRequests).Error; err != nil {
		return nil, err
	}
	return pendingRequests, nil
}

func (r *CircleRepository) AcceptJoinRequest(c context.Context, circleID, requestID int) error {

	return r.db.WithContext(c).Model(&cModel.UserCircle{}).Where("circle_id = ? AND id = ?", circleID, requestID).Update("is_active", true).Error
}

func (r *CircleRepository) GetUserCircles(c context.Context, userID int) ([]*cModel.CircleDetail, error) {
	var circles []*cModel.CircleDetail
	if err := r.db.WithContext(c).Raw("SELECT circles.*, user_circles.role as role, user_circles.created_at uc_created_at  FROM circles Left JOIN user_circles on circles.id = user_circles.circle_id WHERE user_circles.user_id = ? ORDER BY uc_created_at desc", userID).Scan(&circles).Error; err != nil {
		return nil, err
	}
	return circles, nil
}

func (r *CircleRepository) DeleteUserFromCircle(c context.Context, circleID, userID int) error {
	return r.db.WithContext(c).Where("circle_id = ? AND user_id = ?", circleID, userID).Delete(&cModel.UserCircle{}).Error
}

func (r *CircleRepository) ChangeUserRole(c context.Context, circleID, userID int, role cModel.Role) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		query := tx.WithContext(c)
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var membership cModel.UserCircle
		if err := query.Where("circle_id = ? AND user_id = ?", circleID, userID).First(&membership).Error; err != nil {
			return err
		}
		if membership.IsActive && membership.Role == cModel.UserRoleAdmin && string(role) != string(cModel.UserRoleAdmin) {
			var admins []cModel.UserCircle
			adminQuery := tx.WithContext(c)
			if tx.Dialector.Name() == "postgres" {
				adminQuery = adminQuery.Clauses(clause.Locking{Strength: "UPDATE"})
			}
			if err := adminQuery.
				Where("circle_id = ? AND is_active = ? AND role = ?", circleID, true, cModel.UserRoleAdmin).
				Order("id").Find(&admins).Error; err != nil {
				return err
			}
			if len(admins) <= 1 {
				return ErrLastAdmin
			}
		}
		return tx.WithContext(c).Model(&membership).Update("role", role).Error
	})
}

func (r *CircleRepository) GetUserCircleRole(c context.Context, circleID, userID int) (cModel.Role, error) {
	var role cModel.Role
	err := r.db.WithContext(c).Model(&cModel.UserCircle{}).Select("role").Where("circle_id = ? AND user_id = ?", circleID, userID).Scan(&role).Error
	return role, err
}

func (r *CircleRepository) GetActiveUserCircleRole(c context.Context, circleID, userID int) (cModel.Role, error) {
	var membership cModel.UserCircle
	err := r.db.WithContext(c).Select("role").Where("circle_id = ? AND user_id = ? AND is_active = ?", circleID, userID, true).First(&membership).Error
	return cModel.Role(membership.Role), err
}

func (r *CircleRepository) GetCircleByInviteCode(c context.Context, inviteCode string) (*cModel.Circle, error) {
	var circle cModel.Circle
	if err := r.db.WithContext(c).Where("invite_code = ?", inviteCode).First(&circle).Error; err != nil {
		return nil, err
	}
	return &circle, nil
}

func (r *CircleRepository) GetCircleByID(c context.Context, circleID int) (*cModel.Circle, error) {
	var circle cModel.Circle
	if err := r.db.WithContext(c).First(&circle, circleID).Error; err != nil {
		return nil, err
	}
	return &circle, nil
}

func (r *CircleRepository) LeaveCircleByUserID(c context.Context, circleID, userID int) error {
	return r.db.WithContext(c).Where("circle_id = ? AND user_id = ? AND role != 'admin'", circleID, userID).Delete(&cModel.UserCircle{}).Error
}

func (r *CircleRepository) GetUserOriginalCircle(c context.Context, userID int) (int, error) {
	var circleID int
	if err := r.db.WithContext(c).Raw("SELECT circle_id FROM user_circles WHERE user_id = ? AND role = 'admin'", userID).Scan(&circleID).Error; err != nil {
		return 0, err
	}
	return circleID, nil
}

func (r *CircleRepository) DeleteMemberByID(c context.Context, circleID, userID int) error {
	return r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		query := tx.WithContext(c)
		if tx.Dialector.Name() == "postgres" {
			query = query.Clauses(clause.Locking{Strength: "UPDATE"})
		}
		var membership cModel.UserCircle
		if err := query.Where("circle_id = ? AND user_id = ?", circleID, userID).First(&membership).Error; err != nil {
			return err
		}
		if membership.IsActive && membership.Role == cModel.UserRoleAdmin {
			var admins []cModel.UserCircle
			if err := query.Where("circle_id = ? AND is_active = ? AND role = ?", circleID, true, cModel.UserRoleAdmin).
				Order("id").Find(&admins).Error; err != nil {
				return err
			}
			if len(admins) <= 1 {
				return ErrLastAdmin
			}
		}
		return tx.WithContext(c).Delete(&membership).Error
	})
}

func (r *CircleRepository) GetCircleAdmins(c context.Context, circleID int) ([]*cModel.UserCircleDetail, error) {
	var circleAdmins []*cModel.UserCircleDetail
	if err := r.db.WithContext(c).Raw("SELECT * FROM user_circles LEFT JOIN users on users.id = user_circles.user_id WHERE user_circles.circle_id = ? AND user_circles.role = 'admin' AND user_circles.is_active = true", circleID).Scan(&circleAdmins).Error; err != nil {
		return nil, err
	}
	return circleAdmins, nil
}

func (r *CircleRepository) GetDefaultCircle(c context.Context, userID int) (*cModel.Circle, error) {
	var circle cModel.Circle
	if err := r.db.WithContext(c).
		Raw("SELECT circles.* FROM circles LEFT JOIN user_circles ON circles.id = user_circles.circle_id WHERE user_circles.user_id = ? AND user_circles.role = 'admin' ORDER BY user_circles.created_at ASC LIMIT 1", userID).
		Scan(&circle).Error; err != nil {
		return nil, err
	}
	return &circle, nil
}

func (r *CircleRepository) AssignDefaultCircle(c context.Context, userID int) error {
	defaultCircle, err := r.GetDefaultCircle(c, userID)
	if err != nil {
		return err
	}

	return r.db.WithContext(c).Model(&uModel.User{}).Where("id = ?", userID).Update("circle_id", defaultCircle.ID).Error
}

func (r *CircleRepository) RedeemPoints(c context.Context, circleID int, userID int, points int, createdBy int) error {
	logger := logging.FromContext(c)
	err := r.db.Transaction(func(tx *gorm.DB) error {

		result := tx.Model(&cModel.UserCircle{}).
			Where("user_id = ? AND circle_id = ? AND points - points_redeemed - points_adjusted >= ?", userID, circleID, points).
			Update("points_redeemed", gorm.Expr("points_redeemed + ?", points))
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return ErrInsufficientPoints
		}
		if err := tx.Create(&pModel.PointsHistory{
			Action:    pModel.PointsHistoryActionRedeem,
			CircleID:  circleID,
			UserID:    userID,
			Points:    points,
			CreatedAt: time.Now().UTC(),
			CreatedBy: createdBy,
		}).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		logger.Error("Error redeeming points", err)
		return err
	}
	return nil
}

func (r *CircleRepository) ResetPoints(c context.Context, circleID int, userID *int, createdBy int) (int, error) {
	resetCount := 0
	err := r.db.WithContext(c).Transaction(func(tx *gorm.DB) error {
		query := tx.Where("circle_id = ?", circleID)
		if userID != nil {
			query = query.Where("user_id = ?", *userID)
		}
		var memberships []cModel.UserCircle
		if err := query.Find(&memberships).Error; err != nil {
			return err
		}
		if userID != nil && len(memberships) == 0 {
			return gorm.ErrRecordNotFound
		}
		for _, membership := range memberships {
			balance := membership.CurrentBalance()
			if balance == 0 {
				continue
			}
			zero := 0
			if err := tx.Model(&cModel.UserCircle{}).Where("id = ? AND circle_id = ?", membership.ID, circleID).Update("points_adjusted", gorm.Expr("points_adjusted + ?", balance)).Error; err != nil {
				return err
			}
			if err := tx.Create(&pModel.PointsHistory{Action: pModel.PointsHistoryActionReset, CircleID: circleID, UserID: membership.UserID, Points: -balance, PreviousBalance: &balance, ResultingBalance: &zero, CreatedAt: time.Now().UTC(), CreatedBy: createdBy}).Error; err != nil {
				return err
			}
			resetCount++
		}
		return nil
	})
	return resetCount, err
}

func (r *CircleRepository) GetPointsHistory(c context.Context, circleID int, userID *int) ([]pModel.PointsHistory, error) {
	var history []pModel.PointsHistory
	query := r.db.WithContext(c).Where("circle_id = ?", circleID)
	if userID != nil {
		query = query.Where("user_id = ?", *userID)
	}
	err := query.Order("created_at DESC, id DESC").Limit(200).Find(&history).Error
	return history, err
}

func (r *CircleRepository) SetWebhookURL(c context.Context, circleID int, webhookURL *string) error {
	return r.db.WithContext(c).Model(&cModel.Circle{}).Where("id = ?", circleID).Update("webhook_url", webhookURL).Error
}
