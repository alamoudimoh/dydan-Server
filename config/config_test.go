package config

import (
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func clearConfigSelection(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"DYDAN_ENV", "DYDAN_CONFIG_FILE", "DYDAN_DATA_DIR", "DYDAN_JWT_SECRET", "DYDAN_SQLITE_PATH",
		"DYDAN_SERVER_PORT", "DYDAN_REALTIME_ENABLED", "DYDAN_IS_USER_CREATION_DISABLED",
		"DYDAN_EMAIL_HOST", "DYDAN_EMAIL_PORT", "DYDAN_EMAIL_EMAIL", "DYDAN_EMAIL_APP_HOST",
	} {
		t.Setenv(name, "")
	}
}

func TestEmailAppHostEnvironmentMapping(t *testing.T) {
	clearConfigSelection(t)
	t.Setenv("DYDAN_DATA_DIR", t.TempDir())
	t.Setenv("DYDAN_EMAIL_HOST", "smtp.example.com")
	t.Setenv("DYDAN_EMAIL_PORT", "587")
	t.Setenv("DYDAN_EMAIL_EMAIL", "sender@example.com")
	t.Setenv("DYDAN_EMAIL_APP_HOST", "https://app.example.com")

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.EmailConfig.AppHost != "https://app.example.com" {
		t.Fatalf("email app host = %q", cfg.EmailConfig.AppHost)
	}
}

func TestValidateSecurityConfigRejectsPartialEmailConfiguration(t *testing.T) {
	cfg := NewConfig()
	cfg.EmailConfig = EmailConfig{Provider: "smtp", Email: "sender@example.com"}
	if err := validateSecurityConfig(cfg); err == nil {
		t.Fatal("partial SMTP configuration should fail")
	}
}

// TestValidateSecurityConfigRejectsWildcardCORSOrigin pins the CORS hygiene
// rule: browser auth always sends credential-bearing cookies, so a wildcard
// origin would reflect every site's Origin with Allow-Credentials and must
// fail startup instead of silently weakening isolation.
func TestValidateSecurityConfigRejectsWildcardCORSOrigin(t *testing.T) {
	for _, bad := range []string{"*", " * "} {
		cfg := NewConfig()
		cfg.Server.CorsAllowOrigins = []string{"https://app.example", bad}
		if err := validateSecurityConfig(cfg); err == nil {
			t.Fatalf("wildcard cors_allow_origins entry %q should fail validation", bad)
		}
	}
	cfg := NewConfig()
	cfg.Server.CorsAllowOrigins = []string{"https://app.example", "capacitor://localhost"}
	if err := validateSecurityConfig(cfg); err != nil {
		t.Fatalf("explicit origin list must remain valid: %v", err)
	}
}

func TestValidateSecurityConfigNormalizesProviderAndValidatesRedirects(t *testing.T) {
	cfg := NewConfig()
	cfg.EmailConfig = EmailConfig{
		Provider:                " SMTP2GO ",
		Email:                   "sender@example.com",
		Key:                     "secret",
		AppHost:                 "https://app.example",
		VerifyTemplateID:        "verify",
		ResetPasswordTemplateID: "reset",
	}
	cfg.OAuth2Config.AllowedRedirectURLs = []string{"https://app.example/auth/oauth2"}
	if err := validateSecurityConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if cfg.EmailConfig.Provider != "smtp2go" {
		t.Fatalf("provider was not normalized: %q", cfg.EmailConfig.Provider)
	}

	cfg.OAuth2Config.AllowedRedirectURLs = []string{"https://user@app.example/auth/oauth2"}
	if err := validateSecurityConfig(cfg); err == nil {
		t.Fatal("redirect URL with userinfo should fail")
	}
}

func TestZeroConfigDefaultsAndPersistentSecret(t *testing.T) {
	clearConfigSelection(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	t.Setenv("DYDAN_DATA_DIR", dataDir)

	first, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "dydan" || first.Database.Type != "sqlite" || !first.Database.Migration {
		t.Fatalf("database defaults not populated: %#v", first.Database)
	}
	if first.Database.Path != filepath.Join(dataDir, "dydan.db") {
		t.Fatalf("database path=%q", first.Database.Path)
	}
	if first.Server.Port != 7171 || !first.Server.ServeFrontend {
		t.Fatalf("server defaults not populated: %#v", first.Server)
	}
	if first.IsUserCreationDisabled || !first.RealTimeConfig.Enabled || !first.RealTimeConfig.SSEEnabled {
		t.Fatalf("product defaults not populated: signupDisabled=%v realtime=%#v", first.IsUserCreationDisabled, first.RealTimeConfig)
	}
	if first.SchedulerJobs.DueJob != 30*time.Minute || first.SchedulerJobs.OverdueJob != 3*time.Hour || first.SchedulerJobs.PreDueJob != 3*time.Hour {
		t.Fatalf("scheduler defaults not populated: %#v", first.SchedulerJobs)
	}
	if first.Storage.StorageType != "local" || first.Storage.BasePath != filepath.Join(dataDir, "uploads") {
		t.Fatalf("storage defaults not populated: %#v", first.Storage)
	}
	if first.Storage.MaxUserStorage != int64(10*1024*1024*1024) {
		t.Fatalf("max user storage default = %d", first.Storage.MaxUserStorage)
	}
	if len(first.Jwt.Secret) < 32 || strings.Contains(strings.ToLower(first.Jwt.Secret), "change_this") {
		t.Fatal("generated JWT secret is missing or weak")
	}
	secretInfo, err := os.Stat(filepath.Join(dataDir, "jwt-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && secretInfo.Mode().Perm() != 0o600 {
		t.Fatalf("secret mode=%o", secretInfo.Mode().Perm())
	}

	second, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if second.Jwt.Secret != first.Jwt.Secret {
		t.Fatal("restart generated a different JWT secret")
	}

	otherDir := filepath.Join(t.TempDir(), "data")
	t.Setenv("DYDAN_DATA_DIR", otherDir)
	third, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if third.Jwt.Secret == first.Jwt.Secret {
		t.Fatal("different data directories reused a JWT secret")
	}
}

func TestExplicitSecretOverrideDoesNotCreateSecretFile(t *testing.T) {
	clearConfigSelection(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	explicit := "2Bz7nx2zwX0H2j31b3g48zxBCQfhf4pHkYb1EcE01Z8="
	t.Setenv("DYDAN_DATA_DIR", dataDir)
	t.Setenv("DYDAN_JWT_SECRET", explicit)

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Jwt.Secret != explicit {
		t.Fatal("explicit JWT secret was not respected")
	}
	if _, err := os.Stat(filepath.Join(dataDir, "jwt-secret")); !os.IsNotExist(err) {
		t.Fatalf("explicit secret unexpectedly created a secret file: %v", err)
	}
}

func TestConfigPrecedence(t *testing.T) {
	clearConfigSelection(t)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "selfhosted.yaml")
	contents := []byte("name: selfhosted\ndata_dir: " + filepath.Join(tempDir, "yaml-data") + "\nserver:\n  port: 3030\nrealtime:\n  enabled: false\njwt:\n  secret: yaml-secret-that-is-at-least-thirty-two-characters\n")
	if err := os.WriteFile(configPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYDAN_CONFIG_FILE", configPath)
	t.Setenv("DYDAN_SERVER_PORT", "4040")

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "selfhosted" || cfg.Server.Port != 4040 {
		t.Fatalf("config precedence failed: name=%q port=%d", cfg.Name, cfg.Server.Port)
	}
	if cfg.RealTimeConfig.Enabled {
		t.Fatal("explicit YAML false was replaced by a true default")
	}
	if cfg.Jwt.Secret != "yaml-secret-that-is-at-least-thirty-two-characters" {
		t.Fatal("YAML JWT secret was not loaded")
	}
}

func TestWeakExplicitSecretFailsClearly(t *testing.T) {
	clearConfigSelection(t)
	t.Setenv("DYDAN_DATA_DIR", t.TempDir())
	t.Setenv("DYDAN_JWT_SECRET", "secret")
	if _, err := LoadConfigE(); err == nil || (!strings.Contains(err.Error(), "at least 32") && !strings.Contains(err.Error(), "too weak")) {
		t.Fatalf("weak explicit secret error=%v", err)
	}
}

func TestGeneratedSecretIsNotLogged(t *testing.T) {
	clearConfigSelection(t)
	t.Setenv("DYDAN_DATA_DIR", t.TempDir())
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	original := os.Stdout
	os.Stdout = writer
	cfg, loadErr := LoadConfigE()
	os.Stdout = original
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if loadErr != nil {
		t.Fatal(loadErr)
	}
	if strings.Contains(string(output), cfg.Jwt.Secret) {
		t.Fatal("generated JWT secret was written to stdout")
	}
}

func TestUnavailableDataDirectoryFails(t *testing.T) {
	clearConfigSelection(t)
	parent := t.TempDir()
	notDirectory := filepath.Join(parent, "not-a-directory")
	if err := os.WriteFile(notDirectory, []byte("occupied"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYDAN_DATA_DIR", notDirectory)
	if _, err := LoadConfigE(); err == nil || !strings.Contains(err.Error(), "create data directory") {
		t.Fatalf("unavailable data directory error=%v", err)
	}
}

func TestZeroConfigStartupWithoutConfigFiles(t *testing.T) {
	clearConfigSelection(t)
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	t.Setenv("DYDAN_DATA_DIR", dataDir)
	t.Chdir(tempDir)

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "dydan" {
		t.Fatalf("name=%q", cfg.Name)
	}
	if cfg.Server.Port != 7171 {
		t.Fatalf("port=%d", cfg.Server.Port)
	}
}

func TestWebhookSigningSecretAutoCreatedAndStable(t *testing.T) {
	clearConfigSelection(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	t.Setenv("DYDAN_DATA_DIR", dataDir)

	first, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if len(first.WebhookConfig.SigningSecret) < 32 {
		t.Fatal("auto-created webhook signing secret missing or weak")
	}
	// Must differ from every other credential family.
	if first.WebhookConfig.SigningSecret == first.Jwt.Secret || first.WebhookConfig.SigningSecret == first.Storage.MediaSecret {
		t.Fatal("webhook signing secret collides with another credential family")
	}
	secretInfo, err := os.Stat(filepath.Join(dataDir, "webhook-signing-secret"))
	if err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" && secretInfo.Mode().Perm() != 0o600 {
		t.Fatalf("webhook secret mode=%o", secretInfo.Mode().Perm())
	}

	second, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if second.WebhookConfig.SigningSecret != first.WebhookConfig.SigningSecret {
		t.Fatal("restart regenerated the webhook signing secret")
	}
}

func TestWebhookSigningSecretRejectsWeakOrCollidingValues(t *testing.T) {
	clearConfigSelection(t)
	dataDir := filepath.Join(t.TempDir(), "data")
	t.Setenv("DYDAN_DATA_DIR", dataDir)
	t.Setenv("DYDAN_WEBHOOK_SIGNING_SECRET", "short")

	if _, err := LoadConfigE(); err == nil {
		t.Fatal("16-char-minimum not enforced for webhook.signing_secret")
	}

	clearConfigSelection(t)
	t.Setenv("DYDAN_DATA_DIR", dataDir)
	jwt := strings.Repeat("x", 40)
	t.Setenv("DYDAN_JWT_SECRET", jwt)
	t.Setenv("DYDAN_WEBHOOK_SIGNING_SECRET", jwt)
	if _, err := LoadConfigE(); err == nil {
		t.Fatal("webhook.signing_secret equal to jwt.secret must be rejected")
	}
}

func TestStripeWebhookToleranceValidation(t *testing.T) {
	cfg := NewConfig()
	cfg.StripeConfig.WebhookTolerance = -5 * time.Second
	if err := validateSecurityConfig(cfg); err == nil {
		t.Fatal("negative stripe.webhook_tolerance accepted")
	}
	cfg.StripeConfig.WebhookTolerance = 48 * time.Hour
	if err := validateSecurityConfig(cfg); err == nil {
		t.Fatal("stripe.webhook_tolerance above 24h accepted")
	}
	cfg.StripeConfig.WebhookTolerance = webhookToleranceOK
	if err := validateSecurityConfig(cfg); err != nil {
		t.Fatalf("valid tolerance rejected: %v", err)
	}
}

const webhookToleranceOK = 5 * time.Minute

func TestStripeWebhookSecretFileLoading(t *testing.T) {
	clearConfigSelection(t)
	dir := t.TempDir()
	t.Setenv("DYDAN_DATA_DIR", filepath.Join(dir, "data"))
	secretFile := filepath.Join(dir, "whsec")
	if err := os.WriteFile(secretFile, []byte("whsec_from_file_1234567890\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	configFile := filepath.Join(dir, "dydan.yaml")
	if err := os.WriteFile(configFile, []byte("stripe:\n  webhook_secret_file: "+secretFile+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DYDAN_CONFIG_FILE", configFile)
	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.StripeConfig.WebhookSecret != "whsec_from_file_1234567890" {
		t.Fatalf("webhook secret from file = %q", cfg.StripeConfig.WebhookSecret)
	}
}
