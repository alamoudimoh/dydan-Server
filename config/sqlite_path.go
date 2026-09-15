package config

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
)

const (
	dydanDBFilename    = "dydan.db"
	daydanDBFilename   = "daydan.db"
	taskierDBFilename  = "taskier.db"
	donetickDBFilename = "donetick.db"
)

func resolveDefaultSQLitePath(dataDir string, useLegacyWorkingDirectory bool) (string, error) {
	canonical := filepath.Join(dataDir, dydanDBFilename)
	if databaseFileExists(canonical) {
		return canonical, nil
	}

	legacyCandidates := []string{
		filepath.Join(dataDir, daydanDBFilename),
		filepath.Join(dataDir, taskierDBFilename),
		filepath.Join(dataDir, donetickDBFilename),
	}
	if useLegacyWorkingDirectory {
		legacyCandidates = append(legacyCandidates, donetickDBFilename)
	}

	for _, legacyPath := range legacyCandidates {
		if !databaseFileExists(legacyPath) {
			continue
		}
		migrated, err := migrateLegacySQLiteDatabase(legacyPath, canonical)
		if err != nil {
			return "", fmt.Errorf("migrate legacy SQLite database from %s to %s: %w", legacyPath, canonical, err)
		}
		if migrated {
			log.Printf("Dydan: migrated legacy SQLite database from %s to %s", legacyPath, canonical)
		}
		if databaseFileExists(canonical) {
			return canonical, nil
		}
		return "", fmt.Errorf("legacy SQLite migration from %s did not create %s", legacyPath, canonical)
	}

	return canonical, nil
}

func databaseFileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

func migrateLegacySQLiteDatabase(legacyPath, targetPath string) (bool, error) {
	if databaseFileExists(targetPath) {
		return false, nil
	}
	if !databaseFileExists(legacyPath) {
		return false, fmt.Errorf("legacy database %s does not exist", legacyPath)
	}

	if err := copySQLiteDatabaseFiles(legacyPath, targetPath); err != nil {
		removeSQLiteDatabaseFiles(targetPath)
		return false, err
	}

	info, err := os.Stat(targetPath)
	if err != nil || info.Size() == 0 {
		removeSQLiteDatabaseFiles(targetPath)
		return false, fmt.Errorf("migrated database %s is missing or empty", targetPath)
	}

	return true, nil
}

func copySQLiteDatabaseFiles(sourcePath, targetPath string) error {
	if err := copyDatabaseFile(sourcePath, targetPath); err != nil {
		return err
	}
	for _, suffix := range []string{"-wal", "-shm"} {
		companionSource := sourcePath + suffix
		if !databaseFileExists(companionSource) {
			continue
		}
		if err := copyDatabaseFile(companionSource, targetPath+suffix); err != nil {
			return err
		}
	}
	return nil
}

func copyDatabaseFile(sourcePath, targetPath string) error {
	sourceInfo, err := os.Stat(sourcePath)
	if err != nil {
		return err
	}
	if sourceInfo.IsDir() {
		return fmt.Errorf("%s is a directory", sourcePath)
	}

	source, err := os.Open(sourcePath)
	if err != nil {
		return err
	}
	defer source.Close()

	target, err := os.OpenFile(targetPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, sourceInfo.Mode().Perm())
	if err != nil {
		return err
	}
	defer target.Close()

	if _, err := io.Copy(target, source); err != nil {
		return err
	}
	return target.Sync()
}

func removeSQLiteDatabaseFiles(path string) {
	for _, candidate := range []string{path, path + "-wal", path + "-shm"} {
		if err := os.Remove(candidate); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("Dydan: failed to remove incomplete SQLite file %s: %v", candidate, err)
		}
	}
}
