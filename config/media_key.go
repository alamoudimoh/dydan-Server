package config

import "encoding/hex"

// ValidateMediaKeyForTest exposes resolveMediaSigningKey's explicit-secret
// checks (length, JWT-key equality) to other packages' tests.
func ValidateMediaKeyForTest(cfg *Config) error {
	return resolveMediaSigningKey(cfg)
}

// DeriveMediaKeyHex returns the hex-encoded media signing key that
// zero-config installs derive from the given JWT secret. Exposed so operators
// and tests can predict the derived key without booting the server.
func DeriveMediaKeyHex(jwtSecret []byte) (string, error) {
	derived, err := deriveMediaSigningKey(jwtSecret)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(derived), nil
}

// DeriveLegacyMediaKeyHex returns the DayDan-derived key used only to verify
// still-valid URLs minted before the Dydan identity migration.
func DeriveLegacyMediaKeyHex(jwtSecret []byte) (string, error) {
	derived, err := deriveLegacyMediaSigningKey(jwtSecret)
	if err != nil {
		return "", err
	}
	return hex.EncodeToString(derived), nil
}
