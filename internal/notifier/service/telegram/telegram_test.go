package telegram

import (
	"testing"

	"github.com/alamoudimoh/dydan-server/config"
)

func TestNewTelegramNotifierSkipsEmptyToken(t *testing.T) {
	if notifier := NewTelegramNotifier(&config.Config{}); notifier != nil {
		t.Fatal("empty Telegram token must not initialize a notifier")
	}
}
