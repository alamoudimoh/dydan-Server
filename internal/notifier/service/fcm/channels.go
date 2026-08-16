package fcm

const (
	// DayDanFCMChannelID is the canonical Android notification channel for new pushes.
	DayDanFCMChannelID = "daydan_notifications"
	// LegacyDonetickFCMChannelID remains registered on clients for already-installed apps.
	LegacyDonetickFCMChannelID = "donetick_notifications"
)

func androidNotificationChannelID() string {
	return DayDanFCMChannelID
}
