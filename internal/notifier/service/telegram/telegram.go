package telegram

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	"github.com/alamoudimoh/daydan-server/config"
	chModel "github.com/alamoudimoh/daydan-server/internal/chore/model"
	nModel "github.com/alamoudimoh/daydan-server/internal/notifier/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/alamoudimoh/daydan-server/logging"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type TelegramNotifier struct {
	bot *tgbotapi.BotAPI
}

func NewTelegramNotifier(config *config.Config) *TelegramNotifier {
	bot, err := tgbotapi.NewBotAPI(config.Telegram.Token)
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
