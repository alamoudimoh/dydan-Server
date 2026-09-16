package config

import (
	"path/filepath"
)

const dydanDBFilename = "dydan.db"

func resolveDefaultSQLitePath(dataDir string, _ bool) (string, error) {
	return filepath.Join(dataDir, dydanDBFilename), nil
}
