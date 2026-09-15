package config

import "os"

// Environment variable precedence for product-specific overrides:
// DYDAN_* > DAYDAN_* > TASKIER_* > DONETICK_* / DT_* > built-in defaults.

func firstEnvironment(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

func envConfigFile() string {
	return firstEnvironment("DYDAN_CONFIG_FILE", "DAYDAN_CONFIG_FILE", "TASKIER_CONFIG_FILE", "DONETICK_CONFIG_FILE", "DT_CONFIG_FILE")
}

func envProfile() string {
	return firstEnvironment("DYDAN_ENV", "DAYDAN_ENV", "TASKIER_ENV", "DONETICK_ENV", "DT_ENV")
}

func envDataDir() string {
	return firstEnvironment("DYDAN_DATA_DIR", "DAYDAN_DATA_DIR", "TASKIER_DATA_DIR", "DONETICK_DATA_DIR")
}

func envJWTSecret() string {
	return firstEnvironment("DYDAN_JWT_SECRET", "DAYDAN_JWT_SECRET", "TASKIER_JWT_SECRET", "DONETICK_JWT_SECRET", "DT_JWT_SECRET")
}

func envStorageMediaSecret() string {
	return firstEnvironment("DYDAN_STORAGE_MEDIA_SECRET", "DAYDAN_STORAGE_MEDIA_SECRET", "TASKIER_STORAGE_MEDIA_SECRET", "DONETICK_STORAGE_MEDIA_SECRET", "DT_STORAGE_MEDIA_SECRET")
}

// envWebhookSigningSecret allows operators to supply the outbound webhook
// HMAC key without persisting it in the config file.
func envWebhookSigningSecret() string {
	return firstEnvironment("DYDAN_WEBHOOK_SIGNING_SECRET", "DAYDAN_WEBHOOK_SIGNING_SECRET", "TASKIER_WEBHOOK_SIGNING_SECRET", "DONETICK_WEBHOOK_SIGNING_SECRET", "DT_WEBHOOK_SIGNING_SECRET")
}

// envStripeWebhookSecret mirrors stripe.webhook_secret for container
// deployments that inject secrets through the environment.
func envStripeWebhookSecret() string {
	return firstEnvironment("DYDAN_STRIPE_WEBHOOK_SECRET", "DAYDAN_STRIPE_WEBHOOK_SECRET", "TASKIER_STRIPE_WEBHOOK_SECRET", "DONETICK_STRIPE_WEBHOOK_SECRET", "DT_STRIPE_WEBHOOK_SECRET")
}

func envSQLitePath() string {
	return firstEnvironment("DYDAN_SQLITE_PATH", "DAYDAN_SQLITE_PATH", "TASKIER_SQLITE_PATH", "DONETICK_SQLITE_PATH", "DT_SQLITE_PATH")
}

func envTelegramToken() string {
	return firstEnvironment("DYDAN_TELEGRAM_TOKEN", "DAYDAN_TELEGRAM_TOKEN", "TASKIER_TELEGRAM_TOKEN", "DONETICK_TELEGRAM_TOKEN")
}

func envPushoverToken() string {
	return firstEnvironment("DYDAN_PUSHOVER_TOKEN", "DAYDAN_PUSHOVER_TOKEN", "TASKIER_PUSHOVER_TOKEN", "DONETICK_PUSHOVER_TOKEN")
}

func envDisableSignup() string {
	return firstEnvironment("DYDAN_DISABLE_SIGNUP", "DAYDAN_DISABLE_SIGNUP", "TASKIER_DISABLE_SIGNUP", "DONETICK_DISABLE_SIGNUP")
}

func envAllowLegacyOAuthState() bool {
	return firstEnvironment("DYDAN_OAUTH2_ALLOW_LEGACY_STATE", "DAYDAN_OAUTH2_ALLOW_LEGACY_STATE", "TASKIER_OAUTH2_ALLOW_LEGACY_STATE", "DONETICK_OAUTH2_ALLOW_LEGACY_STATE", "DT_OAUTH2_ALLOW_LEGACY_STATE") == "true"
}

func envLogLevel() string {
	return firstEnvironment("DYDAN_LOG_LEVEL", "DAYDAN_LOG_LEVEL", "TASKIER_LOG_LEVEL", "DONETICK_LOG_LEVEL")
}

func envLogEncoding() string {
	return firstEnvironment("DYDAN_LOG_ENCODING", "DAYDAN_LOG_ENCODING", "TASKIER_LOG_ENCODING", "DONETICK_LOG_ENCODING")
}

func envLogDevelopment() string {
	return firstEnvironment("DYDAN_LOG_DEVELOPMENT", "DAYDAN_LOG_DEVELOPMENT", "TASKIER_LOG_DEVELOPMENT", "DONETICK_LOG_DEVELOPMENT")
}

func environmentIsTrue(names ...string) bool {
	return firstEnvironment(names...) == "true"
}
