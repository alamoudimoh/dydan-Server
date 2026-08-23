// daydanctl is the DayDan operator toolkit for self-hosted instances.
//
// It provides safe SQLite online backup/restore plus retention management:
//
//	daydanctl backup  --data-dir /data --out /backups [--keep 7]
//	daydanctl verify  /backups/daydan-backup-20260823-120000
//	daydanctl restore --backup /backups/daydan-backup-20260823-120000 \
//	                  --data-dir /data [--force]
//	daydanctl prune   --root /backups --keep 7
//
// Backups use VACUUM INTO (SQLite's supported consistent online snapshot) so
// DayDan keeps running and WAL contents are included. A manifest.json records
// timestamp, server version, integrity result, file names, sizes, and SHA-256
// checksums. The JWT secret is copied but never logged.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const (
	jwtSecretFilename = "jwt-secret"
	manifestFilename  = "manifest.json"
	backupPrefix      = "daydan-backup-"
)

type fileEntry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

type manifest struct {
	Timestamp    string      `json:"timestamp"`
	Version      string      `json:"version"`
	DatabaseFile string      `json:"database_file"`
	Integrity    string      `json:"db_integrity_check"`
	Files        []fileEntry `json:"files"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintf(os.Stderr, "daydanctl: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: daydanctl <backup|verify|restore|prune> [flags]")
	}
	switch args[0] {
	case "backup":
		return cmdBackup(args[1:])
	case "verify":
		return cmdVerify(args[1:])
	case "restore":
		return cmdRestore(args[1:])
	case "prune":
		return cmdPrune(args[1:])
	case "version":
		fmt.Println(appVersion)
		return nil
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

// appVersion reports the build version; overridable via -ldflags.
var appVersion = func() string {
	v := os.Getenv("DAYDAN_VERSION")
	if v == "" {
		v = "dev"
	}
	return v
}()

func cmdBackup(args []string) error {
	fs := flag.NewFlagSet("backup", flag.ExitOnError)
	dataDir := fs.String("data-dir", "/data", "DayDan data directory")
	out := fs.String("out", "/backups", "directory that receives the timestamped backup")
	keep := fs.Int("keep", 0, "optional retention: keep only the N newest backups in -out")
	if err := fs.Parse(args); err != nil {
		return err
	}

	dbPath := resolveDBPath(*dataDir)
	if dbPath == "" {
		return fmt.Errorf("no SQLite database found in %s", *dataDir)
	}

	stamp := time.Now().UTC().Format("20060102-150405")
	dest := filepath.Join(*out, backupPrefix+stamp)
	if err := os.MkdirAll(dest, 0o700); err != nil {
		return err
	}

	fmt.Printf("daydanctl: backing up %s -> %s\n", dbPath, dest)

	// Consistent online snapshot. VACUUM INTO reads a coherent view of the
	// database (including committed WAL content) while writers keep working;
	// it never copies a live WAL file piecemeal.
	snapshot := filepath.Join(dest, filepath.Base(dbPath))
	if err := vacuumInto(dbPath, snapshot); err != nil {
		return fmt.Errorf("online snapshot failed: %w", err)
	}

	integrity, err := integrityCheck(snapshot)
	if err != nil {
		return fmt.Errorf("integrity check on snapshot failed: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("snapshot integrity_check returned %q; refusing to record backup", integrity)
	}

	m := manifest{
		Timestamp:    time.Now().UTC().Format(time.RFC3339),
		Version:      appVersion,
		DatabaseFile: filepath.Base(dbPath),
		Integrity:    integrity,
	}

	if err := addFileToManifest(&m, *dataDir, dest, snapshot); err != nil {
		return err
	}
	// JWT secret (contents are never printed).
	if err := copySecret(filepath.Join(*dataDir, jwtSecretFilename), filepath.Join(dest, jwtSecretFilename)); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("copy %s failed: %w", jwtSecretFilename, err)
		}
		fmt.Println("daydanctl: note: no jwt-secret file present (secret may be configured externally)")
	} else if err := addFileToManifest(&m, *dataDir, dest, filepath.Join(dest, jwtSecretFilename)); err != nil {
		return err
	}
	// Uploads tree.
	uploadsSrc := filepath.Join(*dataDir, "uploads")
	if info, err := os.Stat(uploadsSrc); err == nil && info.IsDir() {
		if err := copyTree(uploadsSrc, filepath.Join(dest, "uploads")); err != nil {
			return fmt.Errorf("copy uploads failed: %w", err)
		}
		if err := walkTree(filepath.Join(dest, "uploads"), func(path string) error {
			return addFileToManifest(&m, dest, dest, path)
		}); err != nil {
			return err
		}
	}

	mf, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dest, manifestFilename), append(mf, '\n'), 0o600); err != nil {
		return err
	}

	fmt.Printf("daydanctl: backup complete (%d files recorded, db integrity %s)\n", len(m.Files), m.Integrity)

	if *keep > 0 {
		return pruneBackups(*out, *keep)
	}
	return nil
}

func cmdVerify(args []string) error {
	if len(args) != 1 {
		return errors.New("usage: daydanctl verify <backup-directory>")
	}
	m, _, err := validateBackup(args[0])
	if err != nil {
		return err
	}
	fmt.Printf("daydanctl: backup OK (%s, version %s, db integrity %s, %d files)\n",
		m.Timestamp, m.Version, m.Integrity, len(m.Files))
	return nil
}

func cmdRestore(args []string) error {
	fs := flag.NewFlagSet("restore", flag.ExitOnError)
	backup := fs.String("backup", "", "backup directory to restore from")
	dataDir := fs.String("data-dir", "/data", "DayDan data directory")
	force := fs.Bool("force", false, "override the running-instance safety check")
	noSafety := fs.Bool("no-safety-backup", false, "skip preserving current live data (dangerous)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *backup == "" {
		return errors.New("--backup is required")
	}

	m, entries, err := validateBackup(*backup)
	if err != nil {
		return fmt.Errorf("refusing restore: %w", err)
	}

	liveDB := resolveDBPath(*dataDir)
	if liveDB == "" {
		// Fresh/empty target directory (disaster-recovery onto a new volume):
		// nothing to probe or preserve.
		liveDB = filepath.Join(*dataDir, m.DatabaseFile)
	}

	// Never restore over an apparently-running instance without --force.
	if !*force {
		if err := probeNoActiveWriter(liveDB); err != nil {
			return fmt.Errorf("database appears to be in use (is DayDan still running? required downtime applies); use --force to override: %w", err)
		}
	}

	stamp := time.Now().UTC().Format("20060102-150405")

	// Preserve the live data unless explicitly overridden.
	if !*noSafety {
		safety := filepath.Join(*dataDir, fmt.Sprintf("pre-restore-%s", stamp))
		fmt.Printf("daydanctl: preserving current live data in %s\n", safety)
		if err := os.MkdirAll(safety, 0o700); err != nil {
			return err
		}
		for _, name := range []string{filepath.Base(liveDB), filepath.Base(liveDB) + "-wal", filepath.Base(liveDB) + "-shm", jwtSecretFilename} {
			if err := moveIfExists(filepath.Join(*dataDir, name), filepath.Join(safety, name)); err != nil {
				return err
			}
		}
		if err := copyTree(filepath.Join(*dataDir, "uploads"), filepath.Join(safety, "uploads")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	// Restore DB + WAL hygiene.
	for _, name := range []string{filepath.Base(liveDB), filepath.Base(liveDB) + "-wal", filepath.Base(liveDB) + "-shm"} {
		_ = os.Remove(filepath.Join(*dataDir, name))
	}
	for _, e := range entries {
		target := filepath.Join(*dataDir, e.Path)
		if strings.HasPrefix(e.Path, "uploads/") || e.Path == "uploads" {
			target = filepath.Join(*dataDir, e.Path)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return err
		}
		src := filepath.Join(*backup, e.Path)
		if fi, err := os.Stat(src); err == nil && fi.IsDir() {
			continue
		}
		if err := copyFileMode(src, target, 0o600); err != nil {
			return err
		}
		fmt.Printf("daydanctl: restored %s\n", e.Path)
	}
	// Post-restore integrity check of the live file.
	integrity, err := integrityCheck(liveDB)
	if err != nil || integrity != "ok" {
		return fmt.Errorf("post-restore integrity check failed (%q): %v", integrity, err)
	}
	fmt.Printf("daydanctl: restore complete; start DayDan and verify login/data before deleting any pre-restore-* directory\n")
	return nil
}

func cmdPrune(args []string) error {
	fs := flag.NewFlagSet("prune", flag.ExitOnError)
	root := fs.String("root", "/backups", "backup root directory")
	keep := fs.Int("keep", 7, "number of newest backups to keep")
	if err := fs.Parse(args); err != nil {
		return err
	}
	return pruneBackups(*root, *keep)
}

// ---------- helpers ----------

func resolveDBPath(dataDir string) string {
	candidates := []string{"daydan.db", "taskier.db", "donetick.db"}
	for _, c := range candidates {
		p := filepath.Join(dataDir, c)
		if info, err := os.Stat(p); err == nil && !info.IsDir() {
			return p
		}
	}
	return ""
}

func vacuumInto(dbPath, outPath string) error {
	gdb, err := gorm.Open(sqlite.Open(dbPath+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		return err
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		return err
	}
	defer sqlDB.Close()
	_ = os.Remove(outPath)
	return gdb.Exec(fmt.Sprintf("VACUUM INTO '%s'", strings.ReplaceAll(outPath, "'", "''"))).Error
}

func openRO(path string) (*sql.DB, error) {
	return sql.Open("sqlite", path+"?mode=ro&_pragma=busy_timeout(5000)")
}

func integrityCheck(path string) (string, error) {
	db, err := openRO(path)
	if err != nil {
		return "", err
	}
	defer db.Close()
	var result string
	if err := db.QueryRow("PRAGMA integrity_check").Scan(&result); err != nil {
		return "", err
	}
	return strings.ToLower(strings.TrimSpace(result)), nil
}

func sha256File(path string) (string, int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", 0, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), n, nil
}

func addFileToManifest(m *manifest, _root, relRoot, path string) error {
	rel, err := filepath.Rel(relRoot, path)
	if err != nil {
		return err
	}
	sum, size, err := sha256File(path)
	if err != nil {
		return err
	}
	m.Files = append(m.Files, fileEntry{Path: filepath.ToSlash(rel), Size: size, SHA256: sum})
	return nil
}

func copySecret(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	mode := info.Mode().Perm()
	if mode&0o077 != 0 {
		mode = 0o600
	}
	return copyFileMode(src, dst, mode)
}

func copyFileMode(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Chmod(dst, mode)
}

func walkTree(root string, fn func(string) error) error {
	return filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		return fn(path)
	})
}

func copyTree(src, dst string) error {
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return fmt.Errorf("%s is not a directory", src)
	}
	return filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o700)
		}
		return copyFileMode(path, target, 0o600)
	})
}

func readManifest(dir string) (*manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, manifestFilename))
	if err != nil {
		return nil, fmt.Errorf("missing or unreadable %s: %w", manifestFilename, err)
	}
	var m manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil, fmt.Errorf("corrupt %s: %w", manifestFilename, err)
	}
	return &m, nil
}

// validateBackup checks structure, checksums, and DB integrity of a backup.
func validateBackup(dir string) (*manifest, []fileEntry, error) {
	m, err := readManifest(dir)
	if err != nil {
		return nil, nil, err
	}
	if m.Timestamp == "" || m.DatabaseFile == "" {
		return nil, nil, errors.New("manifest missing required fields (timestamp/database_file)")
	}
	snapshot := filepath.Join(dir, m.DatabaseFile)
	if _, err := os.Stat(snapshot); err != nil {
		return nil, nil, fmt.Errorf("database snapshot missing: %w", err)
	}
	if m.Integrity != "" && m.Integrity != "ok" {
		return nil, nil, fmt.Errorf("recorded integrity %q is not ok", m.Integrity)
	}
	// Re-verify checksums now; refuse obviously tampered/incomplete backups.
	for _, e := range m.Files {
		path := filepath.Join(dir, filepath.FromSlash(e.Path))
		sum, size, err := sha256File(path)
		if err != nil {
			return nil, nil, fmt.Errorf("backup file %s unreadable: %w", e.Path, err)
		}
		if sum != e.SHA256 || size != e.Size {
			return nil, nil, fmt.Errorf("checksum mismatch for %s", e.Path)
		}
	}
	// Live integrity re-check of the snapshot itself.
	integrity, err := integrityCheck(snapshot)
	if err != nil {
		return nil, nil, fmt.Errorf("cannot run integrity check: %w", err)
	}
	if integrity != "ok" {
		return nil, nil, fmt.Errorf("snapshot integrity_check returned %q", integrity)
	}
	return m, m.Files, nil
}

// probeNoActiveWriter attempts BEGIN IMMEDIATE with zero busy timeout: when
// another process holds the SQLite write lock the statement fails quickly,
// indicating a likely-running instance.
func probeNoActiveWriter(dbPath string) error {
	db, err := sql.Open("sqlite", dbPath+"?_pragma=busy_timeout(0)")
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	conn, err := db.Conn(ctx)
	if err != nil {
		return err
	}
	defer conn.Close()
	if _, err := conn.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("write lock held by another process: %w", err)
	}
	_, _ = conn.ExecContext(ctx, "ROLLBACK")
	return nil
}

func moveIfExists(src, dst string) error {
	if _, err := os.Stat(src); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return os.Rename(src, dst)
}

func listBackups(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var dirs []string
	for _, e := range entries {
		if e.IsDir() && strings.HasPrefix(e.Name(), backupPrefix) {
			dirs = append(dirs, filepath.Join(root, e.Name()))
		}
	}
	return dirs, nil
}

func pruneBackups(root string, keep int) error {
	dirs, err := listBackups(root)
	if err != nil {
		return err
	}
	// Timestamped names sort chronologically.
	if len(dirs) <= keep {
		return nil
	}
	old := dirs[:len(dirs)-keep]
	for _, dir := range old {
		fmt.Printf("daydanctl: pruning old backup %s\n", dir)
		if err := os.RemoveAll(dir); err != nil {
			return err
		}
	}
	return nil
}
