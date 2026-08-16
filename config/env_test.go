package config

import (
	"os"
	"path/filepath"
	"testing"
)

func clearDayDanEnvironment(t *testing.T) {
	t.Helper()
	for _, name := range []string{
		"DAYDAN_ENV", "DAYDAN_CONFIG_FILE", "DAYDAN_DATA_DIR", "DAYDAN_JWT_SECRET", "DAYDAN_SQLITE_PATH",
		"DAYDAN_TELEGRAM_TOKEN", "DAYDAN_PUSHOVER_TOKEN", "DAYDAN_DISABLE_SIGNUP",
		"DAYDAN_LOG_LEVEL", "DAYDAN_LOG_ENCODING", "DAYDAN_LOG_DEVELOPMENT",
		"DT_ENV", "DT_CONFIG_FILE", "DT_JWT_SECRET", "DT_SQLITE_PATH",
		"TASKIER_CONFIG_FILE", "TASKIER_DATA_DIR", "TASKIER_JWT_SECRET", "TASKIER_SQLITE_PATH",
		"DONETICK_TELEGRAM_TOKEN", "DONETICK_PUSHOVER_TOKEN", "DONETICK_DISABLE_SIGNUP",
		"DONETICK_LOG_LEVEL", "DONETICK_LOG_ENCODING", "DONETICK_LOG_DEVELOPMENT",
	} {
		t.Setenv(name, "")
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
