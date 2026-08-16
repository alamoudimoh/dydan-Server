package user

import "testing"

func TestAuthProviderDayDanPreservesLegacyNumericValue(t *testing.T) {
	if AuthProviderDayDan != 0 {
		t.Fatalf("AuthProviderDayDan = %d, want 0 for database compatibility", AuthProviderDayDan)
	}
	if AuthProviderOAuth2 != 1 || AuthProviderGoogle != 2 || AuthProviderApple != 3 {
		t.Fatalf(
			"auth provider order changed: oauth2=%d google=%d apple=%d",
			AuthProviderOAuth2,
			AuthProviderGoogle,
			AuthProviderApple,
		)
	}
}
