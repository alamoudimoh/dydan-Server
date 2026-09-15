package migrations

import (
	"context"
	"path/filepath"
	"testing"

	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// TestHashAPITokenSecretsMigrationOnLegacyFixture simulates upgrading an
// existing installation whose api_tokens rows predate hashing.
func TestHashAPITokenSecretsMigrationOnLegacyFixture(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(
		filepath.ToSlash(filepath.Join(t.TempDir(), "legacy-tokens.db"))+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := db.AutoMigrate(&uModel.APIToken{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	legacyRows := []uModel.APIToken{
		{UserID: 1, Name: "old-1", Token: "aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111aaaa1111"},
		{UserID: 2, Name: "old-2", Token: "bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222bbbb2222"},
	}
	for i := range legacyRows {
		if err := db.Create(&legacyRows[i]).Error; err != nil {
			t.Fatalf("seed legacy row: %v", err)
		}
	}

	m := HashAPITokenSecrets20260824{}
	ctx := context.Background()

	if err := m.Up(ctx, db); err != nil {
		t.Fatalf("Up on legacy fixture: %v", err)
	}

	var after []uModel.APIToken
	if err := db.Order("id").Find(&after).Error; err != nil {
		t.Fatal(err)
	}
	if len(after) != len(legacyRows) {
		t.Fatalf("row count changed during migration: %d -> %d", len(legacyRows), len(after))
	}
	for i, row := range after {
		want := "sha256:" + legacyRows[i].Token // not the real digest; just ensure prefix + non-plaintext below
		_ = want
		if row.Token == legacyRows[i].Token {
			t.Fatalf("row %s still stores plaintext", row.Name)
		}
		if len(row.Token) < 7 || row.Token[:7] != "sha256:" {
			t.Fatalf("row %s is not in hashed representation: %q", row.Name, row.Token)
		}
	}

	// Idempotent: re-running must be a clean no-op.
	if err := m.Up(ctx, db); err != nil {
		t.Fatalf("second Up: %v", err)
	}
	var again []uModel.APIToken
	if err := db.Order("id").Find(&again).Error; err != nil {
		t.Fatal(err)
	}
	for i := range again {
		if again[i].Token != after[i].Token {
			t.Fatalf("re-run mutated row %s: %q -> %q", again[i].Name, after[i].Token, again[i].Token)
		}
	}

	// Fresh-install behavior: no api_tokens table means no-op success.
	fresh, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "fresh.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := m.Up(ctx, fresh); err != nil {
		t.Fatalf("Up without table must no-op: %v", err)
	}
}
