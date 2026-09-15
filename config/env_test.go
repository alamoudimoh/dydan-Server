package config

import (
	"os"
	"path/filepath"
	"testing"
)

func clearDayDanEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"DYDAN_ENV", "DYDAN_CONFIG_FILE", "DYDAN_DATA_DIR", "DYDAN_JWT_SECRET", "DYDAN_SQLITE_PATH",
		"DYDAN_TELEGRAM_TOKEN", "DYDAN_PUSHOVER_TOKEN", "DYDAN_DISABLE_SIGNUP",
		"DYDAN_LOG_LEVEL", "DYDAN_LOG_ENCODING", "DYDAN_LOG_DEVELOPMENT",
		"DYDAN_SINGLE_CIRCLE_INSTANCE", "DYDAN_DISABLE_PASSWORD_AUTH",
		"DYDAN_OAUTH2_ADMIN_GROUPS", "DYDAN_OAUTH2_MANAGER_GROUPS",
		"DAYDAN_ENV", "DAYDAN_CONFIG_FILE", "DAYDAN_DATA_DIR", "DAYDAN_JWT_SECRET", "DAYDAN_SQLITE_PATH",
		"DAYDAN_TELEGRAM_TOKEN", "DAYDAN_PUSHOVER_TOKEN", "DAYDAN_DISABLE_SIGNUP",
		"DAYDAN_LOG_LEVEL", "DAYDAN_LOG_ENCODING", "DAYDAN_LOG_DEVELOPMENT",
		"DAYDAN_SINGLE_CIRCLE_INSTANCE", "DAYDAN_DISABLE_PASSWORD_AUTH",
		"DAYDAN_OAUTH2_ADMIN_GROUPS", "DAYDAN_OAUTH2_MANAGER_GROUPS",
		"DT_ENV", "DT_CONFIG_FILE", "DT_JWT_SECRET", "DT_SQLITE_PATH",
		"DT_SINGLE_CIRCLE_INSTANCE", "DT_DISABLE_PASSWORD_AUTH",
		"DT_OAUTH2_ADMIN_GROUPS", "DT_OAUTH2_MANAGER_GROUPS",
		"TASKIER_CONFIG_FILE", "TASKIER_DATA_DIR", "TASKIER_JWT_SECRET", "TASKIER_SQLITE_PATH",
		"DONETICK_TELEGRAM_TOKEN", "DONETICK_PUSHOVER_TOKEN", "DONETICK_DISABLE_SIGNUP",
		"DONETICK_LOG_LEVEL", "DONETICK_LOG_ENCODING", "DONETICK_LOG_DEVELOPMENT",
	} {
		t.Setenv(name, "")
	}
}

func TestDydanEnvironmentPrecedence(t *testing.T) {
	clearDayDanEnvironment(t)
	for _, tc := range []struct {
		name string
		vars []string
	}{
		{"Dydan over DayDan", []string{"DYDAN_DATA_DIR", "DAYDAN_DATA_DIR", "TASKIER_DATA_DIR", "DONETICK_DATA_DIR"}},
		{"DayDan over Taskier", []string{"DAYDAN_DATA_DIR", "TASKIER_DATA_DIR", "DONETICK_DATA_DIR"}},
		{"Taskier over Donetick", []string{"TASKIER_DATA_DIR", "DONETICK_DATA_DIR"}},
		{"Donetick alone", []string{"DONETICK_DATA_DIR"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			clearDayDanEnvironment(t)
			for index, name := range tc.vars {
				t.Setenv(name, filepath.Join(t.TempDir(), string(rune('a'+index))))
			}
			if got, want := envDataDir(), os.Getenv(tc.vars[0]); got != want {
				t.Fatalf("envDataDir()=%q, want %q", got, want)
			}
		})
	}
	if got := envDataDir(); got != "" {
		t.Fatalf("unset envDataDir()=%q, want empty", got)
	}
}

func TestDayDanEnvironmentPrecedenceOverLegacy(t *testing.T) {
	clearDayDanEnvironment(t)
	dataDir := filepath.Join(t.TempDir(), "daydan-data")
	t.Setenv("DAYDAN_DATA_DIR", dataDir)
	t.Setenv("TASKIER_DATA_DIR", filepath.Join(t.TempDir(), "taskier-data"))
	t.Setenv("DAYDAN_JWT_SECRET", "2Bz7nx2zwX0H2j31b3g48zxBCQfhf4pHkYb1EcE01Z8=")
	t.Setenv("TASKIER_JWT_SECRET", "3Cz8ox3AxY1I3k42c4h59AyCDRgig5QIlZc2FdF12A9=")

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != dataDir {
		t.Fatalf("data_dir=%q", cfg.DataDir)
	}
	if cfg.Jwt.Secret != "2Bz7nx2zwX0H2j31b3g48zxBCQfhf4pHkYb1EcE01Z8=" {
		t.Fatalf("jwt secret=%q", cfg.Jwt.Secret)
	}
}

func TestLegacyTaskierEnvironmentStillWorks(t *testing.T) {
	clearDayDanEnvironment(t)
	dataDir := filepath.Join(t.TempDir(), "legacy-data")
	t.Setenv("TASKIER_DATA_DIR", dataDir)
	t.Setenv("DT_JWT_SECRET", "2Bz7nx2zwX0H2j31b3g48zxBCQfhf4pHkYb1EcE01Z8=")

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DataDir != dataDir {
		t.Fatalf("data_dir=%q", cfg.DataDir)
	}
	if cfg.Jwt.Secret != "2Bz7nx2zwX0H2j31b3g48zxBCQfhf4pHkYb1EcE01Z8=" {
		t.Fatalf("jwt secret=%q", cfg.Jwt.Secret)
	}
}

func TestDayDanConfigFilePrecedenceOverLegacy(t *testing.T) {
	clearDayDanEnvironment(t)
	tempDir := t.TempDir()
	daydanPath := filepath.Join(tempDir, "daydan.yaml")
	taskierPath := filepath.Join(tempDir, "taskier.yaml")
	if err := os.WriteFile(daydanPath, []byte(testConfigYAML("from-daydan-env")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(taskierPath, []byte(testConfigYAML("from-taskier-env")), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DAYDAN_CONFIG_FILE", daydanPath)
	t.Setenv("TASKIER_CONFIG_FILE", taskierPath)

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Name != "from-daydan-env" {
		t.Fatalf("name=%q", cfg.Name)
	}
}

func TestDonetickEnvironmentFallbackForTelegramToken(t *testing.T) {
	clearDayDanEnvironment(t)
	tempDir := t.TempDir()
	t.Chdir(tempDir)
	t.Setenv("DONETICK_TELEGRAM_TOKEN", "legacy-telegram-token")

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telegram.Token != "legacy-telegram-token" {
		t.Fatalf("telegram token=%q", cfg.Telegram.Token)
	}
}

func TestDayDanTelegramTokenPrecedenceOverDonetick(t *testing.T) {
	clearDayDanEnvironment(t)
	tempDir := t.TempDir()
	t.Chdir(tempDir)
	t.Setenv("DAYDAN_TELEGRAM_TOKEN", "daydan-telegram-token")
	t.Setenv("DONETICK_TELEGRAM_TOKEN", "legacy-telegram-token")

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Telegram.Token != "daydan-telegram-token" {
		t.Fatalf("telegram token=%q", cfg.Telegram.Token)
	}
}

func TestDayDanAuthConfigurationEnvironmentVariables(t *testing.T) {
	clearDayDanEnvironment(t)
	t.Setenv("DAYDAN_SINGLE_CIRCLE_INSTANCE", "true")
	t.Setenv("DAYDAN_DISABLE_PASSWORD_AUTH", "true")
	t.Setenv("DAYDAN_OAUTH2_ADMIN_GROUPS", "admins, owners")
	t.Setenv("DAYDAN_OAUTH2_MANAGER_GROUPS", "managers")

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SingleCircleInstance {
		t.Fatal("single_circle_instance was not enabled")
	}
	if !cfg.DisablePasswordAuth {
		t.Fatal("disable_password_auth was not enabled")
	}
	if got, want := cfg.OAuth2Config.AdminGroups, []string{"admins", "owners"}; !equalStrings(got, want) {
		t.Fatalf("admin_groups=%q, want %q", got, want)
	}
	if got, want := cfg.OAuth2Config.ManagerGroups, []string{"managers"}; !equalStrings(got, want) {
		t.Fatalf("manager_groups=%q, want %q", got, want)
	}
}

func TestLegacyAuthConfigurationEnvironmentVariablesStillWork(t *testing.T) {
	clearDayDanEnvironment(t)
	t.Setenv("DT_SINGLE_CIRCLE_INSTANCE", "true")
	t.Setenv("DT_DISABLE_PASSWORD_AUTH", "true")
	t.Setenv("DT_OAUTH2_ADMIN_GROUPS", "admins")

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.SingleCircleInstance || !cfg.DisablePasswordAuth || !equalStrings(cfg.OAuth2Config.AdminGroups, []string{"admins"}) {
		t.Fatalf("legacy auth environment variables not applied: %+v", cfg)
	}
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
