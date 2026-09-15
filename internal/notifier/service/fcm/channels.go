package fcm

const (
	// DydanFCMChannelID is the canonical Android notification channel.
	DydanFCMChannelID = "dydan_notifications"
	// LegacyDayDanFCMChannelID remains registered by compatibility clients.
	LegacyDayDanFCMChannelID = "daydan_notifications"
	// LegacyDonetickFCMChannelID remains registered on clients for already-installed apps.
	LegacyDonetickFCMChannelID = "donetick_notifications"
)

func androidNotificationChannelID() string {
	return DydanFCMChannelID
}
