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
		"DAYDAN_ENV", "DAYDAN_CONFIG_FILE", "DAYDAN_DATA_DIR", "DAYDAN_JWT_SECRET", "DAYDAN_SQLITE_PATH",
		"DT_ENV", "DT_CONFIG_FILE", "TASKIER_CONFIG_FILE", "DT_JWT_SECRET",
		"TASKIER_JWT_SECRET", "DT_SQLITE_PATH", "TASKIER_SQLITE_PATH",
		"DT_SERVER_PORT", "DT_REALTIME_ENABLED", "DT_IS_USER_CREATION_DISABLED",
		"DT_EMAIL_HOST", "DT_EMAIL_PORT", "DT_EMAIL_EMAIL", "DT_EMAIL_APP_HOST",
	} {
		t.Setenv(name, "")
	}
}

func TestEmailAppHostEnvironmentMapping(t *testing.T) {
	clearConfigSelection(t)
	t.Setenv("TASKIER_DATA_DIR", t.TempDir())
	t.Setenv("DT_EMAIL_HOST", "smtp.example.com")
	t.Setenv("DT_EMAIL_PORT", "587")
	t.Setenv("DT_EMAIL_EMAIL", "sender@example.com")
	t.Setenv("DT_EMAIL_APP_HOST", "https://app.example.com")

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
	t.Setenv("TASKIER_DATA_DIR", dataDir)

	first, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if first.Name != "daydan" || first.Database.Type != "sqlite" || !first.Database.Migration {
		t.Fatalf("database defaults not populated: %#v", first.Database)
	}
	if first.Database.Path != filepath.Join(dataDir, "daydan.db") {
		t.Fatalf("database path=%q", first.Database.Path)
	}
	if first.Server.Port != 2021 || !first.Server.ServeFrontend {
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
	t.Setenv("TASKIER_DATA_DIR", otherDir)
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
	t.Setenv("TASKIER_DATA_DIR", dataDir)
	t.Setenv("DT_JWT_SECRET", explicit)

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

func TestConfigPrecedenceAndLegacyProfileCompatibility(t *testing.T) {
	clearConfigSelection(t)
	tempDir := t.TempDir()
	configPath := filepath.Join(tempDir, "selfhosted.yaml")
	contents := []byte("name: selfhosted\ndata_dir: " + filepath.Join(tempDir, "yaml-data") + "\nserver:\n  port: 3030\nrealtime:\n  enabled: false\njwt:\n  secret: yaml-secret-that-is-at-least-thirty-two-characters\n")
	if err := os.WriteFile(configPath, contents, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DT_CONFIG_FILE", configPath)
	t.Setenv("DT_SERVER_PORT", "4040")

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
		t.Fatal("legacy YAML JWT secret was not loaded")
	}
}

func TestWeakExplicitSecretFailsClearly(t *testing.T) {
	clearConfigSelection(t)
	t.Setenv("TASKIER_DATA_DIR", t.TempDir())
	t.Setenv("DT_JWT_SECRET", "secret")
	if _, err := LoadConfigE(); err == nil || (!strings.Contains(err.Error(), "at least 32") && !strings.Contains(err.Error(), "too weak")) {
		t.Fatalf("weak explicit secret error=%v", err)
	}
}

func TestGeneratedSecretIsNotLogged(t *testing.T) {
	clearConfigSelection(t)
	t.Setenv("TASKIER_DATA_DIR", t.TempDir())
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
	t.Setenv("TASKIER_DATA_DIR", notDirectory)
	if _, err := LoadConfigE(); err == nil || !strings.Contains(err.Error(), "create data directory") {
		t.Fatalf("unavailable data directory error=%v", err)
	}
}

func TestLegacyDonetickDatabaseMigratesToDaydan(t *testing.T) {
	clearConfigSelection(t)
	dataDir := t.TempDir()
	legacyPath := filepath.Join(dataDir, "donetick.db")
	if err := os.WriteFile(legacyPath, []byte("existing database"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TASKIER_DATA_DIR", dataDir)
	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	daydanPath := filepath.Join(dataDir, "daydan.db")
	if cfg.Database.Path != daydanPath {
		t.Fatalf("database path=%q want %q", cfg.Database.Path, daydanPath)
	}
	if _, err := os.Stat(legacyPath); err != nil {
		t.Fatal("legacy donetick.db was removed")
	}
}

func writeTestConfig(t *testing.T, dir, filename, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

const testConfigJWT = "yaml-secret-that-is-at-least-thirty-two-characters"

func testConfigYAML(name string) string {
	return "name: " + name + "\njwt:\n  secret: " + testConfigJWT + "\n"
}

func TestDayDanConfigDetected(t *testing.T) {
	clearConfigSelection(t)
	tempDir := t.TempDir()
	writeTestConfig(t, tempDir, "daydan.yaml", testConfigYAML("from-daydan-yaml"))
	t.Chdir(tempDir)

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "from-daydan-yaml" {
		t.Fatalf("name=%q", cfg.Name)
	}
}

func TestLegacyTaskierConfigDetectedWhenDayDanAbsent(t *testing.T) {
	clearConfigSelection(t)
	tempDir := t.TempDir()
	writeTestConfig(t, tempDir, "taskier.yml", testConfigYAML("from-taskier-yml"))
	t.Chdir(tempDir)

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "from-taskier-yml" {
		t.Fatalf("name=%q", cfg.Name)
	}
}

func TestDayDanConfigWinsWhenBothExist(t *testing.T) {
	clearConfigSelection(t)
	tempDir := t.TempDir()
	writeTestConfig(t, tempDir, "daydan.yaml", testConfigYAML("from-daydan-yaml"))
	writeTestConfig(t, tempDir, "taskier.yaml", testConfigYAML("from-taskier-yaml"))
	t.Chdir(tempDir)

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "from-daydan-yaml" {
		t.Fatalf("name=%q", cfg.Name)
	}
}

func TestExplicitConfigPathPrecedence(t *testing.T) {
	clearConfigSelection(t)
	tempDir := t.TempDir()
	writeTestConfig(t, tempDir, "daydan.yaml", testConfigYAML("from-daydan-yaml"))
	explicitPath := filepath.Join(tempDir, "explicit.yaml")
	writeTestConfig(t, tempDir, "explicit.yaml", testConfigYAML("from-explicit-yaml"))
	t.Setenv("TASKIER_CONFIG_FILE", explicitPath)

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "from-explicit-yaml" {
		t.Fatalf("name=%q", cfg.Name)
	}
}

func TestZeroConfigStartupWithoutConfigFiles(t *testing.T) {
	clearConfigSelection(t)
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	t.Setenv("TASKIER_DATA_DIR", dataDir)
	t.Chdir(tempDir)

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "daydan" {
		t.Fatalf("name=%q", cfg.Name)
	}
	if cfg.Server.Port != 2021 {
		t.Fatalf("port=%d", cfg.Server.Port)
	}
}

func TestConfigNameFileWithoutExtensionIsIgnored(t *testing.T) {
	clearConfigSelection(t)
	tempDir := t.TempDir()
	dataDir := filepath.Join(tempDir, "data")
	t.Setenv("TASKIER_DATA_DIR", dataDir)
	t.Chdir(tempDir)

	// Regression test for the zero-config container: the runtime image places
	// the "/daydan" binary in the working directory, and viper must not treat
	// it as a config file (config name "daydan" with no extension) and fail
	// parsing it as YAML.
	writeTestConfig(t, tempDir, "daydan", string([]byte{0x00, 0x7f, 'n', 'o', 't', ' ', 'y', 'a', 'm', 'l'}))

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "daydan" {
		t.Fatalf("name=%q", cfg.Name)
	}
}

func TestLegacyHostedCloudConfigKeysStillLoad(t *testing.T) {
	clearConfigSelection(t)
	tempDir := t.TempDir()
	writeTestConfig(
		t,
		tempDir,
		"daydan.yaml",
		"is_done_tick_dot_com: true\n"+
			"donetick_cloud:\n"+
			"  google_client_id: legacy-google\n"+
			"  apple_client_id: legacy-apple\n"+
			"jwt:\n  secret: yaml-secret-that-is-at-least-thirty-two-characters\n",
	)
	t.Chdir(tempDir)

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsDayDanCloud {
		t.Fatal("legacy is_done_tick_dot_com was not loaded")
	}
	if cfg.DayDanCloudConfig.GoogleClientID != "legacy-google" {
		t.Fatalf("google_client_id=%q", cfg.DayDanCloudConfig.GoogleClientID)
	}
	if cfg.DayDanCloudConfig.AppleClientID != "legacy-apple" {
		t.Fatalf("apple_client_id=%q", cfg.DayDanCloudConfig.AppleClientID)
	}
}

func TestDayDanHostedCloudConfigAliasesOverrideLegacyKeys(t *testing.T) {
	clearConfigSelection(t)
	tempDir := t.TempDir()
	writeTestConfig(
		t,
		tempDir,
		"daydan.yaml",
		"is_done_tick_dot_com: false\n"+
			"is_daydan_cloud: true\n"+
			"donetick_cloud:\n"+
			"  google_client_id: legacy-google\n"+
			"daydan_cloud:\n"+
			"  google_client_id: alias-google\n"+
			"  apple_client_id: alias-apple\n"+
			"jwt:\n  secret: yaml-secret-that-is-at-least-thirty-two-characters\n",
	)
	t.Chdir(tempDir)

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.IsDayDanCloud {
		t.Fatal("is_daydan_cloud alias was not applied")
	}
	if cfg.DayDanCloudConfig.GoogleClientID != "alias-google" {
		t.Fatalf("google_client_id=%q", cfg.DayDanCloudConfig.GoogleClientID)
	}
	if cfg.DayDanCloudConfig.AppleClientID != "alias-apple" {
		t.Fatalf("apple_client_id=%q", cfg.DayDanCloudConfig.AppleClientID)
	}
}
