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
		"DT_ENV", "DT_CONFIG_FILE", "TASKIER_CONFIG_FILE", "DT_JWT_SECRET",
		"TASKIER_JWT_SECRET", "DT_SQLITE_PATH", "TASKIER_SQLITE_PATH",
		"DT_SERVER_PORT", "DT_REALTIME_ENABLED", "DT_IS_USER_CREATION_DISABLED",
	} {
		t.Setenv(name, "")
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
	if first.Name != "taskier" || first.Database.Type != "sqlite" || !first.Database.Migration {
		t.Fatalf("database defaults not populated: %#v", first.Database)
	}
	if first.Database.Path != filepath.Join(dataDir, "taskier.db") {
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

func TestLegacySQLiteDatabaseIsReused(t *testing.T) {
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
	if cfg.Database.Path != legacyPath {
		t.Fatalf("legacy database was not reused: got %q want %q", cfg.Database.Path, legacyPath)
	}
}
