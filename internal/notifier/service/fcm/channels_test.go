package fcm

import "testing"

func TestNotificationChannelStaysCompatibleUntilClientRollout(t *testing.T) {
	if androidNotificationChannelID() != LegacyDayDanFCMChannelID {
		t.Fatalf("androidNotificationChannelID() = %q, want %q", androidNotificationChannelID(), LegacyDayDanFCMChannelID)
	}
	if DydanFCMChannelID != "dydan_notifications" {
		t.Fatalf("DydanFCMChannelID = %q", DydanFCMChannelID)
	}
	if LegacyDayDanFCMChannelID != "daydan_notifications" {
		t.Fatalf("LegacyDayDanFCMChannelID = %q", LegacyDayDanFCMChannelID)
	}
}

func TestLegacyDonetickFCMChannelPreservedForCompatibility(t *testing.T) {
	if LegacyDonetickFCMChannelID != "donetick_notifications" {
		t.Fatalf("LegacyDonetickFCMChannelID = %q", LegacyDonetickFCMChannelID)
	}
	if LegacyDonetickFCMChannelID == LegacyDayDanFCMChannelID {
		t.Fatal("legacy and canonical FCM channel IDs must differ")
	}
}
