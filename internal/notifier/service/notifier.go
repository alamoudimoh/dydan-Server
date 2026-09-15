package service

import (
	"context"

	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	nModel "github.com/alamoudimoh/dydan-server/internal/notifier/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
)

type Notifier interface {
	SendChoreCompletion(c context.Context, chore *chModel.Chore, user *uModel.User)
	SendNotification(c context.Context, notification *nModel.NotificationDetails) error
}
