package pushover

import (
	"context"
	"errors"

	"github.com/alamoudimoh/dydan-server/config"
	nModel "github.com/alamoudimoh/dydan-server/internal/notifier/model"
	"github.com/alamoudimoh/dydan-server/logging"
	"github.com/gregdel/pushover"
)

type Pushover struct {
	pushover *pushover.Pushover
}

func NewPushover(cfg *config.Config) *Pushover {

	pushoverApp := pushover.New(cfg.Pushover.Token)

	return &Pushover{
		pushover: pushoverApp,
	}
}

func (p *Pushover) SendNotification(c context.Context, notification *nModel.NotificationDetails) error {
	if notification.TargetID == "" {
		return errors.New("unable to send notification, targetID is empty")
	}
	log := logging.FromContext(c)
	recipient := pushover.NewRecipient(notification.TargetID)
	message := pushover.NewMessageWithTitle(notification.Text, "Dydan")

	_, err := p.pushover.SendMessage(message, recipient)
	if err != nil {
		log.Debug("Error sending pushover notification", err)
		return err
	}

	return nil
}
