package migrations

import (
	"context"

	"github.com/alamoudimoh/dydan-server/internal/user/repo"
	"gorm.io/gorm"
)

type HashAPITokenSecrets20260824 struct{}

func (m HashAPITokenSecrets20260824) ID() string {
	return "20260824_hash_api_token_secrets"
}

func (m HashAPITokenSecrets20260824) Description() string {
	return `Replace plaintext API-token secrets with SHA-256 digests ("sha256:<hex>"). Existing plaintext credentials keep authenticating: they are converted deterministically, and the authentication path accepts both representations during the transition. Idempotent; safe to re-run.`
}

func (m HashAPITokenSecrets20260824) Up(ctx context.Context, db *gorm.DB) error {
	if !db.Migrator().HasTable("api_tokens") {
		// Fresh installations have nothing to migrate; new tokens are created
		// hashed from day one.
		return nil
	}
	_, err := user.HashLegacyAPITokenRows(ctx, db)
	return err
}

// Down cannot restore plaintext secrets — the whole point of the migration is
// that the reusable credential no longer exists at rest. Treated as a no-op,
// mirroring other irreversible migrations in this registry.
func (m HashAPITokenSecrets20260824) Down(ctx context.Context, db *gorm.DB) error {
	return nil
}

func init() {
	Register(HashAPITokenSecrets20260824{})
}
