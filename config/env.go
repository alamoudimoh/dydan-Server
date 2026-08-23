package config

import "os"

// Environment variable precedence for DayDan-specific overrides:
// DAYDAN_* > TASKIER_* > DONETICK_* / DT_* > built-in defaults.

func firstEnvironment(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

func envConfigFile() string {
	return firstEnvironment("DAYDAN_CONFIG_FILE", "TASKIER_CONFIG_FILE", "DT_CONFIG_FILE")
}

func envProfile() string {
	return firstEnvironment("DAYDAN_ENV", "DT_ENV")
}

func envDataDir() string {
	return firstEnvironment("DAYDAN_DATA_DIR", "TASKIER_DATA_DIR")
}

func envJWTSecret() string {
	return firstEnvironment("DAYDAN_JWT_SECRET", "TASKIER_JWT_SECRET", "DT_JWT_SECRET")
}

func envSQLitePath() string {
	return firstEnvironment("DAYDAN_SQLITE_PATH", "TASKIER_SQLITE_PATH", "DT_SQLITE_PATH")
}

func envTelegramToken() string {
	return firstEnvironment("DAYDAN_TELEGRAM_TOKEN", "DONETICK_TELEGRAM_TOKEN")
}

func envPushoverToken() string {
	return firstEnvironment("DAYDAN_PUSHOVER_TOKEN", "DONETICK_PUSHOVER_TOKEN")
}

func envDisableSignup() string {
	return firstEnvironment("DAYDAN_DISABLE_SIGNUP", "DONETICK_DISABLE_SIGNUP")
}

func envAllowLegacyOAuthState() bool {
	return firstEnvironment("DAYDAN_OAUTH2_ALLOW_LEGACY_STATE") == "true"
}

func envLogLevel() string {
	return firstEnvironment("DAYDAN_LOG_LEVEL", "DONETICK_LOG_LEVEL")
}

func envLogEncoding() string {
	return firstEnvironment("DAYDAN_LOG_ENCODING", "DONETICK_LOG_ENCODING")
}

func envLogDevelopment() string {
	return firstEnvironment("DAYDAN_LOG_DEVELOPMENT", "DONETICK_LOG_DEVELOPMENT")
}

func environmentIsTrue(names ...string) bool {
	return firstEnvironment(names...) == "true"
}
