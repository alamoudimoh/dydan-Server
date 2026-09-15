package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/alamoudimoh/dydan-server/config"
	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	nModel "github.com/alamoudimoh/dydan-server/internal/notifier/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/alamoudimoh/dydan-server/logging"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type TelegramNotifier struct {
	bot *tgbotapi.BotAPI
}

func NewTelegramNotifier(cfg *config.Config) *TelegramNotifier {
	// Telegram is optional. An empty token is the zero-config default, and
	// passing it to NewBotAPI makes an unnecessary outbound request during
	// startup that produces a misleading error log.
	if strings.TrimSpace(cfg.Telegram.Token) == "" {
		return nil
	}

	bot, err := tgbotapi.NewBotAPI(cfg.Telegram.Token)
	if err != nil {
		fmt.Println("Error creating bot: ", err)
		return nil
	}

	return &TelegramNotifier{
		bot: bot,
	}
}

func (tn *TelegramNotifier) SendChoreCompletion(c context.Context, chore *chModel.Chore, user *uModel.User) {

	log := logging.FromContext(c)
	if tn == nil {
		log.Error("Telegram bot is not initialized, Skipping sending message")
		return
	}

	targets := []int64{}
	if user.ChatID != 0 {
		targets = append(targets, user.ChatID)
	}
	if chore.NotificationMetadataV2.CircleGroup && chore.NotificationMetadataV2.CircleGroupID != nil {
		// attempt to parse it:

		if *chore.NotificationMetadataV2.CircleGroupID != 0 {
			targets = append(targets, *chore.NotificationMetadataV2.CircleGroupID)
		}

	}

	text := fmt.Sprintf("🎉 *%s* is completed! is off the list, %s! 🌟 ", chore.Name, user.DisplayName)
	for _, target := range targets {
		msg := tgbotapi.NewMessage(target, text)

		msg.ParseMode = "Markdown"
		_, err := tn.bot.Send(msg)
		if err != nil {
			log.Error("Error sending message to user: ", err)
			log.Debug("Error sending message, chore: ", chore.Name, " user: ", user.DisplayName, " chatID: ", user.ChatID, " user id: ", user.ID)
		}
	}

}

func (tn *TelegramNotifier) SendNotification(c context.Context, notification *nModel.NotificationDetails) error {
	log := logging.FromContext(c)
	if notification.TargetID == "" {
		return errors.New("unable to send notification, targetID is empty")
	}
	chatID, err := strconv.ParseInt(notification.TargetID, 10, 64)
	if err != nil {
		log.Error("Error parsing chatID: ", err)
		return err
	}

	msg := tgbotapi.NewMessage(chatID, notification.Text)
	msg.ParseMode = "Markdown"
	_, err = tn.bot.Send(msg)
	if err != nil {
		log.Error("Error sending message to user: ", err)
		log.Debug("Error sending message, notification: ", notification.Text, " chatID: ", chatID)
		return err
	}
	return nil
}
