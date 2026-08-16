package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeDatabaseFile(t *testing.T, path, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestFreshInstallUsesDaydanDatabase(t *testing.T) {
	dataDir := t.TempDir()
	path, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dataDir, daydanDBFilename) {
		t.Fatalf("path=%q", path)
	}
}

func TestExistingDaydanDatabaseWins(t *testing.T) {
	dataDir := t.TempDir()
	daydanPath := filepath.Join(dataDir, daydanDBFilename)
	taskierPath := filepath.Join(dataDir, taskierDBFilename)
	writeDatabaseFile(t, daydanPath, "daydan-data")
	writeDatabaseFile(t, taskierPath, "taskier-data")

	path, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if path != daydanPath {
		t.Fatalf("path=%q", path)
	}
	if _, err := os.Stat(taskierPath); err != nil {
		t.Fatal("legacy taskier.db was removed")
	}
}

func TestExistingTaskierDatabaseMigratesSafely(t *testing.T) {
	dataDir := t.TempDir()
	taskierPath := filepath.Join(dataDir, taskierDBFilename)
	daydanPath := filepath.Join(dataDir, daydanDBFilename)
	payload := "sqlite-taskier-payload"
	writeDatabaseFile(t, taskierPath, payload)

	path, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if path != daydanPath {
		t.Fatalf("path=%q", path)
	}
	got, err := os.ReadFile(daydanPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("migrated contents=%q", string(got))
	}
	if _, err := os.Stat(taskierPath); err != nil {
		t.Fatal("legacy taskier.db was removed")
	}
}

func TestExistingDonetickDatabaseMigratesSafely(t *testing.T) {
	dataDir := t.TempDir()
	legacyPath := filepath.Join(dataDir, donetickDBFilename)
	daydanPath := filepath.Join(dataDir, daydanDBFilename)
	payload := "sqlite-donetick-payload"
	writeDatabaseFile(t, legacyPath, payload)

	path, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if path != daydanPath {
		t.Fatalf("path=%q", path)
	}
	got, err := os.ReadFile(daydanPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("migrated contents=%q", string(got))
	}
}

func TestDaydanAndLegacyBothExistDaydanWinsWithoutMigration(t *testing.T) {
	dataDir := t.TempDir()
	daydanPath := filepath.Join(dataDir, daydanDBFilename)
	taskierPath := filepath.Join(dataDir, taskierDBFilename)
	writeDatabaseFile(t, daydanPath, "canonical")
	writeDatabaseFile(t, taskierPath, "legacy")

	first, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if first != daydanPath || second != daydanPath {
		t.Fatalf("first=%q second=%q", first, second)
	}
}

func TestSQLiteMigrationIsIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	taskierPath := filepath.Join(dataDir, taskierDBFilename)
	daydanPath := filepath.Join(dataDir, daydanDBFilename)
	writeDatabaseFile(t, taskierPath, "legacy-data")

	first, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if first != daydanPath || second != daydanPath {
		t.Fatalf("first=%q second=%q", first, second)
	}
	firstContents, err := os.ReadFile(daydanPath)
	if err != nil {
		t.Fatal(err)
	}
	secondContents, err := os.ReadFile(daydanPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstContents) != string(secondContents) {
		t.Fatal("daydan.db contents changed on second resolve")
	}
}

func TestFailedMigrationRetainsLegacyDatabase(t *testing.T) {
	dataDir := t.TempDir()
	taskierPath := filepath.Join(dataDir, taskierDBFilename)
	daydanPath := filepath.Join(dataDir, daydanDBFilename)
	writeDatabaseFile(t, taskierPath, "legacy-data")
	if err := os.Mkdir(daydanPath, 0o700); err != nil {
		t.Fatal(err)
	}

	path, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if path != taskierPath {
		t.Fatalf("path=%q", path)
	}
}

func TestExplicitDaydanSQLitePathWins(t *testing.T) {
	clearDayDanEnvironment(t)
	dataDir := t.TempDir()
	explicit := filepath.Join(dataDir, "custom-daydan.db")
	writeDatabaseFile(t, explicit, "explicit")
	t.Setenv("DAYDAN_SQLITE_PATH", explicit)

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Path != explicit {
		t.Fatalf("path=%q", cfg.Database.Path)
	}
}

func TestExplicitTaskierSQLitePathStillWorks(t *testing.T) {
	clearDayDanEnvironment(t)
	dataDir := t.TempDir()
	explicit := filepath.Join(dataDir, "custom-taskier.db")
	writeDatabaseFile(t, explicit, "explicit")
	t.Setenv("TASKIER_SQLITE_PATH", explicit)

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Path != explicit {
		t.Fatalf("path=%q", cfg.Database.Path)
	}
}

func TestExplicitDTSQLitePathStillWorks(t *testing.T) {
	clearDayDanEnvironment(t)
	dataDir := t.TempDir()
	explicit := filepath.Join(dataDir, "custom-dt.db")
	writeDatabaseFile(t, explicit, "explicit")
	t.Setenv("DT_SQLITE_PATH", explicit)

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Path != explicit {
		t.Fatalf("path=%q", cfg.Database.Path)
	}
}

func TestDaydanSQLitePathPrecedenceOverLegacyEnv(t *testing.T) {
	clearDayDanEnvironment(t)
	dataDir := t.TempDir()
	daydanExplicit := filepath.Join(dataDir, "daydan-explicit.db")
	taskierExplicit := filepath.Join(dataDir, "taskier-explicit.db")
	t.Setenv("DAYDAN_SQLITE_PATH", daydanExplicit)
	t.Setenv("TASKIER_SQLITE_PATH", taskierExplicit)

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Path != daydanExplicit {
		t.Fatalf("path=%q", cfg.Database.Path)
	}
}

func TestLegacyDatabaseContentsSurviveMigrationThroughLoadConfig(t *testing.T) {
	clearDayDanEnvironment(t)
	dataDir := t.TempDir()
	taskierPath := filepath.Join(dataDir, taskierDBFilename)
	payload := "persistent-legacy-data"
	writeDatabaseFile(t, taskierPath, payload)
	t.Setenv("DAYDAN_DATA_DIR", dataDir)
	t.Setenv("DAYDAN_JWT_SECRET", "2Bz7nx2zwX0H2j31b3g48zxBCQfhf4pHkYb1EcE01Z8=")

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Path != filepath.Join(dataDir, daydanDBFilename) {
		t.Fatalf("path=%q", cfg.Database.Path)
	}
	got, err := os.ReadFile(cfg.Database.Path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("contents=%q", string(got))
	}
}

func TestSQLiteWALCompanionFilesAreCopiedDuringMigration(t *testing.T) {
	dataDir := t.TempDir()
	taskierPath := filepath.Join(dataDir, taskierDBFilename)
	writeDatabaseFile(t, taskierPath, "main")
	writeDatabaseFile(t, taskierPath+"-wal", "wal")
	writeDatabaseFile(t, taskierPath+"-shm", "shm")

	path, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	daydanPath := filepath.Join(dataDir, daydanDBFilename)
	if path != daydanPath {
		t.Fatalf("path=%q", path)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if !databaseFileExists(daydanPath + suffix) {
			t.Fatalf("missing migrated companion %s", daydanPath+suffix)
		}
	}
}
