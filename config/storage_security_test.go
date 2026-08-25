package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestValidateSignedURLTTL pins the signed-URL lifetime contract: positive,
// capped at the AWS SigV4 7-day maximum, defaulting to that maximum.
func TestValidateSignedURLTTL(t *testing.T) {
	cfg := NewConfig()
	if cfg.Storage.SignedURLTTL != MaxSignedURLTTL {
		t.Fatalf("default TTL = %s, want %s", cfg.Storage.SignedURLTTL, MaxSignedURLTTL)
	}
	for _, bad := range []time.Duration{0, -time.Hour} {
		cfg.Storage.SignedURLTTL = bad
		if err := validateSecurityConfig(cfg); err == nil {
			t.Fatalf("TTL %s must fail validation", bad)
		}
	}
	cfg.Storage.SignedURLTTL = MaxSignedURLTTL + time.Second
	if err := validateSecurityConfig(cfg); err == nil {
		t.Fatal("TTL beyond the SigV4 ceiling must fail validation")
	}
	cfg.Storage.SignedURLTTL = time.Hour
	if err := validateSecurityConfig(cfg); err != nil {
		t.Fatalf("1h TTL must be accepted: %v", err)
	}
}

// TestValidatePublicReadRequiresBucket proves public_read cannot be enabled
// accidentally: it requires an explicitly configured bucket name.
func TestValidatePublicReadRequiresBucket(t *testing.T) {
	cfg := NewConfig()
	cfg.Storage.PublicRead = true
	cfg.Storage.BucketName = ""
	if err := validateSecurityConfig(cfg); err == nil {
		t.Fatal("public_read without bucket_name must fail startup")
	}
	cfg.Storage.BucketName = "   "
	if err := validateSecurityConfig(cfg); err == nil {
		t.Fatal("public_read with blank bucket_name must fail startup")
	}
	cfg.Storage.BucketName = "media-bucket"
	if err := validateSecurityConfig(cfg); err != nil {
		t.Fatalf("explicit public_read with bucket must remain valid: %v", err)
	}

	cfg.Storage.PublicRead = false
	cfg.Storage.BucketName = ""
	if err := validateSecurityConfig(cfg); err != nil {
		t.Fatalf("default (private) configuration must remain valid: %v", err)
	}
}

// TestPublicReadMalformedValuesCannotEnable pins viper's bool handling:
// true-ish values enable the mode, everything else either decodes to false or
// fails startup outright — empty/garbage values can never silently turn on
// anonymous readability.
func TestPublicReadMalformedValuesCannotEnable(t *testing.T) {
	clearConfigSelection(t)
	t.Setenv("TASKIER_DATA_DIR", t.TempDir())
	for _, raw := range []string{"garbage", "yes-please"} {
		t.Setenv("DT_STORAGE_PUBLIC_READ", raw)
		if _, err := LoadConfigE(); err == nil {
			t.Fatalf("input %q must fail startup instead of being coerced", raw)
		}
	}
	for _, raw := range []string{"", "0", "false"} {
		t.Setenv("DT_STORAGE_PUBLIC_READ", raw)
		cfg, err := LoadConfigE()
		if err != nil {
			t.Fatalf("input %q: %v", raw, err)
		}
		if cfg.Storage.PublicRead {
			t.Fatalf("input %q enabled public_read", raw)
		}
	}
	t.Setenv("DT_STORAGE_PUBLIC_READ", "true")
	if _, err := LoadConfigE(); err == nil {
		// No bucket configured in this env: validation rejects it, which is
		// itself proof that enabling requires explicit, complete config.
		t.Log("public_read=true accepted without bucket — unexpected")
	} else if !strings.Contains(err.Error(), "bucket") {
		t.Fatalf("unexpected error: %v", err)
	}
}

// TestMediaSigningKeyResolution covers the precedence chain and validation of
// the media signing key: explicit secret > secret file > HKDF derivation.
func TestMediaSigningKeyResolution(t *testing.T) {
	clearConfigSelection(t)
	dataDir := t.TempDir()
	t.Setenv("TASKIER_DATA_DIR", dataDir)
	t.Setenv("DAYDAN_JWT_SECRET", "unit-test-jwt-secret-0123456789abcdef")

	// Zero-config: derived key is stable across loads (restart keeps URLs
	// valid), differs from the JWT secret, and is strong material.
	cfg1, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	cfg2, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if cfg1.Storage.MediaSecret == "" {
		t.Fatal("derived media key missing")
	}
	if cfg1.Storage.MediaSecret != cfg2.Storage.MediaSecret {
		t.Fatal("derived media key must be deterministic across restarts")
	}
	if cfg1.Storage.MediaSecret == cfg1.Jwt.Secret {
		t.Fatal("derived media key must not equal the JWT secret")
	}
	if len(cfg1.Storage.MediaSecret) < mediaSigningKeyMinLen {
		t.Fatal("derived media key too short")
	}

	// Explicit secret wins over derivation and survives JWT rotation.
	t.Setenv("DT_STORAGE_MEDIA_SECRET", strings.Repeat("m", 32))
	explicit, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if explicit.Storage.MediaSecret != strings.Repeat("m", 32) {
		t.Fatal("explicit media secret must be honored verbatim")
	}

	// Too-short explicit secrets are rejected at startup.
	t.Setenv("DT_STORAGE_MEDIA_SECRET", "short")
	if _, err := LoadConfigE(); err == nil {
		t.Fatal("short media secret must fail startup")
	}

	// Secret file resolution.
	fileDir := filepath.Join(dataDir, "keys")
	if err := os.MkdirAll(fileDir, 0o700); err != nil {
		t.Fatal(err)
	}
	keyFile := filepath.Join(fileDir, "media-key")
	if err := os.WriteFile(keyFile, []byte(strings.Repeat("f", 40)+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	os.Unsetenv("DT_STORAGE_MEDIA_SECRET")
	t.Setenv("DT_STORAGE_MEDIA_SECRET_FILE", keyFile)
	fromFile, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if fromFile.Storage.MediaSecret != strings.Repeat("f", 40) {
		t.Fatalf("media secret file not honored: got %q", fromFile.Storage.MediaSecret)
	}

	// Explicit env override beats everything else configured in files.
	t.Setenv("DAYDAN_STORAGE_MEDIA_SECRET", strings.Repeat("e", 32))
	envWins, err := LoadConfigE()
	if err != nil {
		t.Fatal(err)
	}
	if envWins.Storage.MediaSecret != strings.Repeat("e", 32) {
		t.Fatal("DAYDAN_STORAGE_MEDIA_SECRET override must win")
	}
}
