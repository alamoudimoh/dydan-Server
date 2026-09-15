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

func TestFreshInstallUsesDydanDatabase(t *testing.T) {
	dataDir := t.TempDir()
	path, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if path != filepath.Join(dataDir, dydanDBFilename) {
		t.Fatalf("path=%q", path)
	}
}

func TestExistingDydanDatabaseWins(t *testing.T) {
	dataDir := t.TempDir()
	dydanPath := filepath.Join(dataDir, dydanDBFilename)
	daydanPath := filepath.Join(dataDir, daydanDBFilename)
	writeDatabaseFile(t, dydanPath, "dydan-data")
	writeDatabaseFile(t, daydanPath, "daydan-data")

	path, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if path != dydanPath {
		t.Fatalf("path=%q", path)
	}
	if _, err := os.Stat(daydanPath); err != nil {
		t.Fatal("legacy daydan.db was removed")
	}
}

func TestExistingDaydanDatabaseMigratesSafely(t *testing.T) {
	dataDir := t.TempDir()
	daydanPath := filepath.Join(dataDir, daydanDBFilename)
	dydanPath := filepath.Join(dataDir, dydanDBFilename)
	payload := "sqlite-daydan-payload"
	writeDatabaseFile(t, daydanPath, payload)

	path, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if path != dydanPath {
		t.Fatalf("path=%q", path)
	}
	got, err := os.ReadFile(dydanPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != payload {
		t.Fatalf("migrated contents=%q", string(got))
	}
	if _, err := os.Stat(daydanPath); err != nil {
		t.Fatal("legacy daydan.db was removed")
	}
}

func TestExistingDonetickDatabaseMigratesSafely(t *testing.T) {
	dataDir := t.TempDir()
	legacyPath := filepath.Join(dataDir, donetickDBFilename)
	daydanPath := filepath.Join(dataDir, dydanDBFilename)
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
	daydanPath := filepath.Join(dataDir, dydanDBFilename)
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
	dydanPath := filepath.Join(dataDir, dydanDBFilename)
	writeDatabaseFile(t, taskierPath, "legacy-data")

	first, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	second, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if first != dydanPath || second != dydanPath {
		t.Fatalf("first=%q second=%q", first, second)
	}
	firstContents, err := os.ReadFile(dydanPath)
	if err != nil {
		t.Fatal(err)
	}
	secondContents, err := os.ReadFile(dydanPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(firstContents) != string(secondContents) {
		t.Fatal("dydan.db contents changed on second resolve")
	}
}

func TestFailedMigrationRetainsLegacyDatabase(t *testing.T) {
	dataDir := t.TempDir()
	taskierPath := filepath.Join(dataDir, taskierDBFilename)
	dydanPath := filepath.Join(dataDir, dydanDBFilename)
	writeDatabaseFile(t, taskierPath, "legacy-data")
	if err := os.Mkdir(dydanPath, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := resolveDefaultSQLitePath(dataDir, false)
	if err == nil {
		t.Fatal("migration failure must be returned instead of selecting a legacy database")
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
	if cfg.Database.Path != filepath.Join(dataDir, dydanDBFilename) {
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
	dydanPath := filepath.Join(dataDir, dydanDBFilename)
	if path != dydanPath {
		t.Fatalf("path=%q", path)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if !databaseFileExists(dydanPath + suffix) {
			t.Fatalf("missing migrated companion %s", dydanPath+suffix)
		}
	}
}
