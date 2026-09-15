package fcm

const (
	// DydanFCMChannelID is reserved for the client rollout after it registers
	// the channel. Existing clients only know the legacy channel below.
	DydanFCMChannelID = "dydan_notifications"
	// LegacyDayDanFCMChannelID remains the delivery target until AppWeb/mobile
	// registers DydanFCMChannelID on installed devices.
	LegacyDayDanFCMChannelID = "daydan_notifications"
	// LegacyDonetickFCMChannelID remains registered on clients for already-installed apps.
	LegacyDonetickFCMChannelID = "donetick_notifications"
)

func androidNotificationChannelID() string {
	return LegacyDayDanFCMChannelID
}
