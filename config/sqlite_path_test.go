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
	legacyDaydanPath := filepath.Join(dataDir, legacyDaydanDBFilename)
	writeDatabaseFile(t, dydanPath, "dydan-data")
	writeDatabaseFile(t, legacyDaydanPath, "legacy-daydan-data")

	path, err := resolveDefaultSQLitePath(dataDir, false)
	if err != nil {
		t.Fatal(err)
	}
	if path != dydanPath {
		t.Fatalf("path=%q", path)
	}
	if _, err := os.Stat(legacyDaydanPath); err != nil {
		t.Fatal("legacy daydan.db was removed")
	}
}

func TestExistingLegacyDaydanDatabaseMigratesSafely(t *testing.T) {
	dataDir := t.TempDir()
	legacyDaydanPath := filepath.Join(dataDir, legacyDaydanDBFilename)
	dydanPath := filepath.Join(dataDir, dydanDBFilename)
	payload := "sqlite-daydan-payload"
	writeDatabaseFile(t, legacyDaydanPath, payload)

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
	if _, err := os.Stat(legacyDaydanPath); err != nil {
		t.Fatal("legacy daydan.db was removed")
	}
}

func TestExistingLegacyDonetickDatabaseMigratesSafely(t *testing.T) {
	dataDir := t.TempDir()
	legacyPath := filepath.Join(dataDir, legacyDonetickDBFilename)
	dydanPath := filepath.Join(dataDir, dydanDBFilename)
	payload := "sqlite-donetick-payload"
	writeDatabaseFile(t, legacyPath, payload)

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
}

func TestDydanAndLegacyBothExistDydanWinsWithoutMigration(t *testing.T) {
	dataDir := t.TempDir()
	dydanPath := filepath.Join(dataDir, dydanDBFilename)
	legacyTaskierPath := filepath.Join(dataDir, legacyTaskierDBFilename)
	writeDatabaseFile(t, dydanPath, "canonical")
	writeDatabaseFile(t, legacyTaskierPath, "legacy")

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
}

func TestSQLiteMigrationIsIdempotent(t *testing.T) {
	dataDir := t.TempDir()
	legacyTaskierPath := filepath.Join(dataDir, legacyTaskierDBFilename)
	dydanPath := filepath.Join(dataDir, dydanDBFilename)
	writeDatabaseFile(t, legacyTaskierPath, "legacy-data")

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
	legacyTaskierPath := filepath.Join(dataDir, legacyTaskierDBFilename)
	dydanPath := filepath.Join(dataDir, dydanDBFilename)
	writeDatabaseFile(t, legacyTaskierPath, "legacy-data")
	if err := os.Mkdir(dydanPath, 0o700); err != nil {
		t.Fatal(err)
	}

	_, err := resolveDefaultSQLitePath(dataDir, false)
	if err == nil {
		t.Fatal("migration failure must be returned instead of selecting a legacy database")
	}
}

func TestExplicitLegacyDaydanSQLitePathStillWorks(t *testing.T) {
	clearConfigEnvironment(t)
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

func TestExplicitLegacyTaskierSQLitePathStillWorks(t *testing.T) {
	clearConfigEnvironment(t)
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
	clearConfigEnvironment(t)
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

func TestLegacyDaydanSQLitePathPrecedenceOverOlderEnv(t *testing.T) {
	clearConfigEnvironment(t)
	dataDir := t.TempDir()
	legacyDaydanExplicit := filepath.Join(dataDir, "daydan-explicit.db")
	legacyTaskierExplicit := filepath.Join(dataDir, "taskier-explicit.db")
	t.Setenv("DAYDAN_SQLITE_PATH", legacyDaydanExplicit)
	t.Setenv("TASKIER_SQLITE_PATH", legacyTaskierExplicit)

	cfg, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Database.Path != legacyDaydanExplicit {
		t.Fatalf("path=%q", cfg.Database.Path)
	}
}

func TestLegacyDatabaseContentsSurviveMigrationThroughLoadConfig(t *testing.T) {
	clearConfigEnvironment(t)
	dataDir := t.TempDir()
	legacyTaskierPath := filepath.Join(dataDir, legacyTaskierDBFilename)
	payload := "persistent-legacy-data"
	writeDatabaseFile(t, legacyTaskierPath, payload)
	t.Setenv("DYDAN_DATA_DIR", dataDir)
	t.Setenv("DYDAN_JWT_SECRET", "2Bz7nx2zwX0H2j31b3g48zxBCQfhf4pHkYb1EcE01Z8=")

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
	legacyTaskierPath := filepath.Join(dataDir, legacyTaskierDBFilename)
	writeDatabaseFile(t, legacyTaskierPath, "main")
	writeDatabaseFile(t, legacyTaskierPath+"-wal", "wal")
	writeDatabaseFile(t, legacyTaskierPath+"-shm", "shm")

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
