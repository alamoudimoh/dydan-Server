package config

import "os"

func firstEnvironment(names ...string) string {
	for _, name := range names {
		if value := os.Getenv(name); value != "" {
			return value
		}
	}
	return ""
}

func envConfigFile() string {
	return firstEnvironment("DYDAN_CONFIG_FILE")
}

func envProfile() string {
	return firstEnvironment("DYDAN_ENV")
}

func envDataDir() string {
	return firstEnvironment("DYDAN_DATA_DIR")
}

func envJWTSecret() string {
	return firstEnvironment("DYDAN_JWT_SECRET")
}

func envStorageMediaSecret() string {
	return firstEnvironment("DYDAN_STORAGE_MEDIA_SECRET")
}

// envWebhookSigningSecret allows operators to supply the outbound webhook
// HMAC key without persisting it in the config file.
func envWebhookSigningSecret() string {
	return firstEnvironment("DYDAN_WEBHOOK_SIGNING_SECRET")
}

// envStripeWebhookSecret mirrors stripe.webhook_secret for container
// deployments that inject secrets through the environment.
func envStripeWebhookSecret() string {
	return firstEnvironment("DYDAN_STRIPE_WEBHOOK_SECRET")
}

func envSQLitePath() string {
	return firstEnvironment("DYDAN_SQLITE_PATH")
}

func envTelegramToken() string {
	return firstEnvironment("DYDAN_TELEGRAM_TOKEN")
}

func envPushoverToken() string {
	return firstEnvironment("DYDAN_PUSHOVER_TOKEN")
}

func envDisableSignup() string {
	return firstEnvironment("DYDAN_DISABLE_SIGNUP")
}

func envAllowLegacyOAuthState() bool {
	return firstEnvironment("DYDAN_OAUTH2_ALLOW_LEGACY_STATE") == "true"
}

func envLogLevel() string {
	return firstEnvironment("DYDAN_LOG_LEVEL")
}

func envLogEncoding() string {
	return firstEnvironment("DYDAN_LOG_ENCODING")
}

func envLogDevelopment() string {
	return firstEnvironment("DYDAN_LOG_DEVELOPMENT")
}

func environmentIsTrue(names ...string) bool {
	return firstEnvironment(names...) == "true"
}
