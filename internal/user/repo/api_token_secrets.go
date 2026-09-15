package user

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/alamoudimoh/dydan-server/logging"
	"gorm.io/gorm"
)

// API tokens are high-entropy uniformly random secrets (128 bits of CSPRNG
// output plus a username/timestamp component), so a plain SHA-256 digest is
// the appropriate credential verifier: there is no low-entropy material for a
// dictionary attack to work on, and a deliberately slow password KDF would
// only add latency to every authenticated request. Digests are stored with an
// explicit "sha256:" discriminator so that
//
//   - a stored digest value can never itself be replayed as a credential
//     (the authentication path rejects inputs in stored format),
//   - the one-time legacy sweep is trivially idempotent (only unprefixed,
//     i.e. legacy plaintext, rows are eligible), and
//   - legacy plaintext rows remain distinguishable while they age out.
//
// Lookup stays a single indexed equality probe: the presented secret is
// digested once and matched against the existing token index. This is the
// same shape as the password-reset token precedent in this repository.
const apiTokenHashPrefix = "sha256:"

// APITokenSecretHash returns the persisted representation of a freshly
// generated secret.
func APITokenSecretHash(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return apiTokenHashPrefix + hex.EncodeToString(sum[:])
}

// isStoredFormatToken reports whether the given stored value is already in
// hashed representation.
func isStoredFormatToken(stored string) bool {
	return strings.HasPrefix(stored, apiTokenHashPrefix)
}

// isWellFormedAPITokenSecret validates the external token format produced by
// CreateLongLivedToken: exactly 64 lowercase hex characters. Inputs in stored
// format ("sha256:...") are rejected outright so a leaked digest value can
// never be replayed as a credential.
func isWellFormedAPITokenSecret(secret string) bool {
	if len(secret) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(secret)
	return err == nil
}

// HashLegacyAPITokenRows converts every remaining plaintext api_tokens.token
// row to hashed representation. It is the body of the registered
// 20260824 migration and is safe to run repeatedly:
//
//   - only unprefixed rows are selected,
//   - each row is rewritten with a guarded conditional update
//     (WHERE id = ? AND token = <old plaintext>) so a concurrent rewrite or a
//     crash mid-sweep can never corrupt or lose a usable token,
//   - the transformation is deterministic, so a partially applied sweep is
//     completed by the next run.
func HashLegacyAPITokenRows(ctx context.Context, db *gorm.DB) (int, error) {
	log := logging.FromContext(ctx)

	type row struct {
		ID    int
		Token string
	}
	var legacy []row
	if err := db.WithContext(ctx).
		Table("api_tokens").
		Select("id, token").
		Where("token NOT LIKE ?", apiTokenHashPrefix+"%").
		Find(&legacy).Error; err != nil {
		return 0, err
	}

	converted := 0
	for _, r := range legacy {
		if r.Token == "" || isStoredFormatToken(r.Token) {
			continue
		}
		hashed := APITokenSecretHash(r.Token)
		res := db.WithContext(ctx).
			Exec("UPDATE api_tokens SET token = ? WHERE id = ? AND token = ?",
				hashed, r.ID, r.Token)
		if res.Error != nil {
			return converted, fmt.Errorf("hash api token %d: %w", r.ID, res.Error)
		}
		converted += int(res.RowsAffected)
		if res.RowsAffected == 0 {
			// Another replica/process rewrote (or deleted) the row first;
			// nothing to do — the guarded predicate kept us correct.
			log.Debugf("api token %d already migrated or removed", r.ID)
		}
	}
	return converted, nil
}

// CountLegacyPlaintextAPITokens reports how many api_tokens rows still hold a
// pre-migration plaintext credential (the operational signal for "is the
// 20260824_hash_api_token_secrets migration fully drained yet"). It exposes a
// count only — never token values or digests — and is safe to call from any
// request path, health endpoint, or replica:
//
//   - read-only, so concurrent sweeps/lazy upgrades cannot corrupt state;
//   - idempotent under repetition;
//   - empty-token rows are excluded because they can never authenticate and
//     are deliberately skipped by the sweep, so counting them as pending work
//     would misreport migration progress forever.
//
// A count of 0 means every stored credential is in hashed representation.
// Rows are matched with NOT LIKE on the digest prefix; api_tokens is small
// (one row per user-created token), so this stays cheap for the rare callers
// that use it (startup log + health endpoint), not per-request hot paths.
func CountLegacyPlaintextAPITokens(ctx context.Context, db *gorm.DB) (int64, error) {
	if !db.WithContext(ctx).Migrator().HasTable("api_tokens") {
		// Fresh installations have nothing legacy to report.
		return 0, nil
	}
	var n int64
	if err := db.WithContext(ctx).
		Table("api_tokens").
		Where("token <> '' AND token NOT LIKE ?", apiTokenHashPrefix+"%").
		Count(&n).Error; err != nil {
		return 0, err
	}
	return n, nil
}

// upgradeLegacyAPIToken atomically rewrites a single legacy plaintext row to
// hashed representation after a successful legacy authentication. The
// conditional predicate makes concurrent upgrades idempotent: whichever
// request wins rewrites the row, losers match zero rows, and every request
// still authenticated against the pre-update value within the same statement
// ordering.
func (r *UserRepository) upgradeLegacyAPIToken(ctx context.Context, storedValue string) {
	if !r.db.WithContext(ctx).Migrator().HasTable("api_tokens") {
		return
	}
	hashed := APITokenSecretHash(storedValue)
	err := r.db.WithContext(ctx).
		Exec("UPDATE api_tokens SET token = ? WHERE token = ? AND token NOT LIKE ?",
			hashed, storedValue, apiTokenHashPrefix+"%").Error
	if err != nil {
		// Upgrading is best-effort: authentication already succeeded, and the
		// eager sweep will convert any leftovers. Never fail a login for it.
		logging.FromContext(ctx).Debugw("lazy api token upgrade skipped", "error", err)
	}
}

// constantTimeTokenEqual compares a presented secret against a stored legacy
// plaintext value in constant time. New-format verification happens via the
// SHA-256 digest equality probe (comparing digests, not secrets), where
// timing side channels are meaningless; this path covers the transitional
// legacy rows so no secret comparison anywhere is timing-sensitive.
func constantTimeTokenEqual(presented, storedLegacy string) bool {
	return subtle.ConstantTimeCompare([]byte(presented), []byte(storedLegacy)) == 1
}
