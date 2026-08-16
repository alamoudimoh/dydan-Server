package discord

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	chModel "github.com/alamoudimoh/daydan-server/internal/chore/model"
	nModel "github.com/alamoudimoh/daydan-server/internal/notifier/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/alamoudimoh/daydan-server/logging"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

type DiscordNotifier struct {
}

func NewDiscordNotifier(config *config.Config) *DiscordNotifier {
	return &DiscordNotifier{}
}

func (dn *DiscordNotifier) SendChoreCompletion(c context.Context, chore *chModel.Chore, user *uModel.User) {
	log := logging.FromContext(c)
	if dn == nil {
		log.Error("Discord notifier is not initialized, skipping message sending")
		return
	}

	message := fmt.Sprintf("🎉 **%s** is completed! Great job, %s! 🌟", chore.Name, user.DisplayName)
	err := dn.sendMessage(c, user.UserNotificationTargets.TargetID, message)
	if err != nil {
		log.Error("Error sending Discord message:", err)
	}
}

func (dn *DiscordNotifier) SendNotification(c context.Context, notification *nModel.NotificationDetails) error {

	if dn == nil {
		return errors.New("Discord notifier is not initialized")
	}

	if notification.Text == "" {
		return errors.New("unable to send notification, text is empty")
	}

	return dn.sendMessage(c, notification.TargetID, notification.Text)
}

func (dn *DiscordNotifier) sendMessage(c context.Context, webhookURL string, message string) error {
	log := logging.FromContext(c)

	if webhookURL == "" {
		return errors.New("unable to send notification, webhook URL is empty")
	}

	payload := map[string]string{"content": message}
	jsonData, err := json.Marshal(payload)
	if err != nil {
		log.Error("Error marshaling JSON:", err)
		return err
	}

	resp, err := httpClient.Post(webhookURL, "application/json", bytes.NewBuffer(jsonData))
	if err != nil {
		log.Error("Error sending message to Discord:", err)
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNoContent && resp.StatusCode != http.StatusOK {
		log.Error("Discord webhook returned unexpected status:", resp.Status)
		return errors.New("failed to send Discord message")
	}

	return nil
}
