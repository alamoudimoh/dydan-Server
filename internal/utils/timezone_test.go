package utils

import "testing"

func TestIsValidTimezoneRequiresIANAIdentity(t *testing.T) {
	for _, zone := range []string{"UTC", "Asia/Riyadh", "America/New_York", "Europe/London"} {
		if !IsValidTimezone(zone) {
			t.Errorf("expected %q to be valid", zone)
		}
	}
	for _, zone := range []string{"", "Mars/Riyadh", "AST", "CST", "GMT+3", "Local"} {
		if IsValidTimezone(zone) {
			t.Errorf("expected %q to be rejected", zone)
		}
	}
}
