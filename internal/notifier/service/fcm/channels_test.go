package fcm

import "testing"

func TestDayDanFCMChannelIsCanonicalForNewNotifications(t *testing.T) {
	if androidNotificationChannelID() != DayDanFCMChannelID {
		t.Fatalf("androidNotificationChannelID() = %q, want %q", androidNotificationChannelID(), DayDanFCMChannelID)
	}
	if DayDanFCMChannelID != "daydan_notifications" {
		t.Fatalf("DayDanFCMChannelID = %q", DayDanFCMChannelID)
	}
}

func TestLegacyDonetickFCMChannelPreservedForCompatibility(t *testing.T) {
	if LegacyDonetickFCMChannelID != "donetick_notifications" {
		t.Fatalf("LegacyDonetickFCMChannelID = %q", LegacyDonetickFCMChannelID)
	}
	if LegacyDonetickFCMChannelID == DayDanFCMChannelID {
		t.Fatal("legacy and canonical FCM channel IDs must differ")
	}
}
