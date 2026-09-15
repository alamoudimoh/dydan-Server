package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func seedDataDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	gdb, err := gorm.Open(sqlite.Open(filepath.Join(dir, "dydan.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	if err := gdb.Exec("CREATE TABLE chores (id INTEGER PRIMARY KEY, title TEXT)").Error; err != nil {
		t.Fatalf("create table: %v", err)
	}
	if err := gdb.Exec("INSERT INTO chores (title) VALUES ('water the plants')").Error; err != nil {
		t.Fatalf("seed row: %v", err)
	}
	sqlDB, _ := gdb.DB()
	_ = sqlDB.Close()

	if err := os.WriteFile(filepath.Join(dir, jwtSecretFilename), []byte("a-very-secure-secret-value-0123456789\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "uploads"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "uploads", "photo.txt"), []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestBackupVerifyRestoreRoundtrip(t *testing.T) {
	dataDir := seedDataDir(t)
	backups := t.TempDir()

	if err := run([]string{"backup", "--data-dir", dataDir, "--out", backups}); err != nil {
		t.Fatalf("backup failed: %v", err)
	}
	dirs, err := listBackups(backups)
	if err != nil || len(dirs) != 1 {
		t.Fatalf("expected exactly one backup, got %v (%v)", dirs, err)
	}
	backupDir := dirs[0]

	// Verify passes on a good backup.
	if err := run([]string{"verify", backupDir}); err != nil {
		t.Fatalf("verify failed: %v", err)
	}

	m, err := readManifest(backupDir)
	if err != nil {
		t.Fatal(err)
	}
	if m.Integrity != "ok" || m.DatabaseFile != "dydan.db" {
		t.Fatalf("bad manifest: %+v", m)
	}
	var hasSecret, hasUpload bool
	for _, f := range m.Files {
		switch f.Path {
		case jwtSecretFilename:
			hasSecret = true
		case "uploads/photo.txt":
			hasUpload = true
		}
	}
	if !hasSecret || !hasUpload {
		t.Fatalf("manifest must cover jwt-secret and uploads: %+v", m.Files)
	}

	// Restore into a fresh target directory.
	restoreDir := t.TempDir()
	if err := run([]string{"restore", "--backup", backupDir, "--data-dir", restoreDir}); err != nil {
		t.Fatalf("restore failed: %v", err)
	}
	integrity, err := integrityCheck(filepath.Join(restoreDir, "dydan.db"))
	if err != nil || integrity != "ok" {
		t.Fatalf("restored db integrity: %q (%v)", integrity, err)
	}
	secret, err := os.ReadFile(filepath.Join(restoreDir, jwtSecretFilename))
	if err != nil || strings.TrimSpace(string(secret)) == "" {
		t.Fatalf("jwt-secret not restored correctly: %q (%v)", secret, err)
	}
	if info, err := os.Stat(filepath.Join(restoreDir, "jwt-secret")); err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("jwt-secret mode wrong: %v", info.Mode().Perm())
	}
	data, err := os.ReadFile(filepath.Join(restoreDir, "uploads", "photo.txt"))
	if err != nil || string(data) != "hello" {
		t.Fatalf("uploads not restored: %q (%v)", data, err)
	}
}

func TestVerifyRejectsTamperedBackup(t *testing.T) {
	dataDir := seedDataDir(t)
	backups := t.TempDir()
	if err := run([]string{"backup", "--data-dir", dataDir, "--out", backups}); err != nil {
		t.Fatal(err)
	}
	backupDir, err := listBackups(backups)
	if err != nil || len(backupDir) != 1 {
		t.Fatalf("listBackups: %v %v", backupDir, err)
	}
	dir := backupDir[0]

	// Tamper with the database snapshot.
	dbPath := filepath.Join(dir, "dydan.db")
	if err := os.WriteFile(dbPath, []byte("not a database"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", dir}); err == nil {
		t.Fatal("verify accepted a corrupted database snapshot")
	}
}

func TestVerifyRejectsMissingManifestAndMissingFiles(t *testing.T) {
	// No manifest at all.
	if err := run([]string{"verify", t.TempDir()}); err == nil {
		t.Fatal("verify accepted directory without manifest")
	}

	dataDir := seedDataDir(t)
	backups := t.TempDir()
	if err := run([]string{"backup", "--data-dir", dataDir, "--out", backups}); err != nil {
		t.Fatal(err)
	}
	dir, err := listBackups(backups)
	if err != nil || len(dir) != 1 {
		t.Fatalf("listBackups: %v %v", dir, err)
	}
	// Remove an uploaded file recorded in the manifest.
	if err := os.Remove(filepath.Join(dir[0], "uploads", "photo.txt")); err != nil {
		t.Fatal(err)
	}
	if err := run([]string{"verify", dir[0]}); err == nil {
		t.Fatal("verify accepted incomplete backup")
	}
}

func TestPruneKeepsNewest(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		name := filepath.Join(root, backupPrefix+string(rune('0'+i)))
		if err := os.MkdirAll(name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneBackups(root, 2); err != nil {
		t.Fatal(err)
	}
	dirs, _ := listBackups(root)
	if len(dirs) != 2 {
		t.Fatalf("expected 2 remaining backups, got %d", len(dirs))
	}
	if !strings.HasSuffix(dirs[1], "4") {
		t.Fatalf("newest backup should survive pruning: %v", dirs)
	}
}

func TestRestoreRefusesInvalidBackup(t *testing.T) {
	bad := t.TempDir()
	dataDir := seedDataDir(t)
	if err := run([]string{"restore", "--backup", bad, "--data-dir", dataDir}); err == nil {
		t.Fatal("restore accepted backup without manifest")
	}
}
