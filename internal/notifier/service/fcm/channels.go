package fcm

const (
	// DydanFCMChannelID is the canonical Android notification channel.
	DydanFCMChannelID = "dydan_notifications"
)

func androidNotificationChannelID() string {
	return DydanFCMChannelID
}
