package user

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newTokenHashHarness(t *testing.T) (*gorm.DB, *UserRepository) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(
		filepath.ToSlash(filepath.Join(t.TempDir(), "token-hash.db"))+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.APIToken{}, &cModel.Circle{}, &uModel.UserNotificationTarget{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db, NewUserRepository(db, &config.Config{})
}

func seedTokenOwner(t *testing.T, repo *UserRepository) *uModel.User {
	t.Helper()
	user := &uModel.User{Username: "token-owner", Email: "owner@example.com"}
	if _, err := repo.CreateUser(context.Background(), user); err != nil {
		t.Fatalf("seed owner: %v", err)
	}
	return user
}

func storedTokenRows(t *testing.T, db *gorm.DB) []uModel.APIToken {
	t.Helper()
	var rows []uModel.APIToken
	if err := db.Find(&rows).Error; err != nil {
		t.Fatalf("read token rows: %v", err)
	}
	return rows
}

func TestStoreAPITokenPersistsDigestNotPlaintext(t *testing.T) {
	db, repo := newTokenHashHarness(t)
	owner := seedTokenOwner(t, repo)

	secret := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	stored, err := repo.StoreAPIToken(context.Background(), owner.ID, "ci", secret)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	rows := storedTokenRows(t, db)
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	if rows[0].Token != APITokenSecretHash(secret) {
		t.Fatalf("stored value must be the prefixed digest, got %q", rows[0].Token)
	}
	if strings.Contains(rows[0].Token, secret) || rows[0].Token == secret {
		t.Fatal("plaintext secret must never be persisted")
	}
	if stored.Token != APITokenSecretHash(secret) {
		t.Fatal("returned model must carry the digest, not the secret")
	}
}

func TestGetUserByTokenNewFormatPaths(t *testing.T) {
	_, repo := newTokenHashHarness(t)
	owner := seedTokenOwner(t, repo)

	secret := "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
	if _, err := repo.StoreAPIToken(context.Background(), owner.ID, "cli", secret); err != nil {
		t.Fatalf("store: %v", err)
	}

	user, err := repo.GetUserByToken(context.Background(), secret)
	if err != nil {
		t.Fatalf("valid token must authenticate: %v", err)
	}
	if user.Username != owner.Username {
		t.Fatalf("authenticated wrong user: %s", user.Username)
	}

	for name, presented := range map[string]string{
		"wrong secret":     "0000000000000000000000000000000000000000000000000000000000000000",
		"malformed short":  "abc",
		"malformed nonhex": "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
		"digest replay":    APITokenSecretHash(secret),
		"prefixed garbage": "sha256:not-a-real-digest-value",
		"empty":            "",
	} {
		if _, err := repo.GetUserByToken(context.Background(), presented); err == nil {
			t.Errorf("%s must not authenticate", name)
		}
	}
}

func TestLegacyPlaintextTokenStillAuthenticatesAndUpgrades(t *testing.T) {
	db, repo := newTokenHashHarness(t)
	owner := seedTokenOwner(t, repo)

	// Simulate a pre-upgrade installation: raw plaintext credential at rest.
	legacy := "1111222233334444555566667777888899990000aaaabbbbccccddddeeeeffff"
	if err := db.Create(&uModel.APIToken{UserID: owner.ID, Name: "legacy", Token: legacy}).Error; err != nil {
		t.Fatalf("seed legacy row: %v", err)
	}

	// Old tokens continue authenticating after deployment.
	user, err := repo.GetUserByToken(context.Background(), legacy)
	if err != nil {
		t.Fatalf("legacy token must keep authenticating: %v", err)
	}
	if user.Username != owner.Username {
		t.Fatalf("wrong user: %s", user.Username)
	}

	// Lazy migration removed the plaintext.
	row := storedTokenRows(t, db)[0]
	if row.Token != APITokenSecretHash(legacy) {
		t.Fatalf("legacy row should have been upgraded to digest form, got %q", row.Token)
	}
	if strings.Contains(row.Token, legacy) {
		t.Fatal("plaintext survived lazy upgrade")
	}

	// And the token keeps working afterwards.
	if _, err := repo.GetUserByToken(context.Background(), legacy); err != nil {
		t.Fatalf("post-upgrade authentication broke: %v", err)
	}
}

func TestHashLegacyAPITokenRowsIdempotent(t *testing.T) {
	db, repo := newTokenHashHarness(t)
	owner := seedTokenOwner(t, repo)

	legacyA := "aaaa0000bbbb1111cccc2222dddd3333eeee4444ffff55556666777788889999"
	alreadyHashed := APITokenSecretHash("eeee0000bbbb1111cccc2222dddd3333eeee4444ffff55556666777788889999")
	if err := db.Create(&uModel.APIToken{UserID: owner.ID, Name: "legacy-a", Token: legacyA}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&uModel.APIToken{UserID: owner.ID, Name: "hashed-b", Token: alreadyHashed}).Error; err != nil {
		t.Fatal(err)
	}

	for round := 1; round <= 2; round++ {
		n, err := HashLegacyAPITokenRows(context.Background(), db)
		if err != nil {
			t.Fatalf("round %d migration: %v", round, err)
		}
		if round == 1 && n != 1 {
			t.Fatalf("round 1 must convert exactly the 1 legacy row, got %d", n)
		}
		if round == 2 && n != 0 {
			t.Fatalf("round 2 must be a no-op, converted %d", n)
		}
	}

	rows := storedTokenRows(t, db)
	want := map[string]string{"legacy-a": APITokenSecretHash(legacyA), "hashed-b": alreadyHashed}
	if len(rows) != 2 {
		t.Fatalf("row count changed: %d", len(rows))
	}
	for _, r := range rows {
		if want[r.Name] != r.Token {
			t.Fatalf("row %s: unexpected stored value %q", r.Name, r.Token)
		}
		if !isStoredFormatToken(r.Token) {
			t.Fatalf("row %s still contains plaintext material", r.Name)
		}
	}

	// Both credentials authenticate after the sweep.
	for _, secret := range []string{legacyA, "eeee0000bbbb1111cccc2222dddd3333eeee4444ffff55556666777788889999"} {
		if _, err := repo.GetUserByToken(context.Background(), secret); err != nil {
			t.Fatalf("credential lost by migration: %v", err)
		}
	}
}

func TestDeletedTokenCannotAuthenticate(t *testing.T) {
	db, repo := newTokenHashHarness(t)
	owner := seedTokenOwner(t, repo)

	secret := "99990000111122223333444455556666777788889999aaaabbbbccccddddeeee"
	if _, err := repo.StoreAPIToken(context.Background(), owner.ID, "tmp", secret); err != nil {
		t.Fatal(err)
	}
	if err := repo.DeleteAPIToken(context.Background(), owner.ID, "1"); err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if _, err := repo.GetUserByToken(context.Background(), secret); err == nil {
		t.Fatal("revoked token must not authenticate")
	}
	if n := len(storedTokenRows(t, db)); n != 0 {
		t.Fatalf("revocation must remove the row, %d left", n)
	}
}

func TestConcurrentLegacyAuthenticationIsSafe(t *testing.T) {
	db, repo := newTokenHashHarness(t)
	owner := seedTokenOwner(t, repo)

	legacy := "121212123434345456567678789a9a9a9abcbcbcdcdededeefefffef0f010101"
	if err := db.Create(&uModel.APIToken{UserID: owner.ID, Name: "shared", Token: legacy}).Error; err != nil {
		t.Fatal(err)
	}

	const workers = 8
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if _, err := repo.GetUserByToken(context.Background(), legacy); err != nil {
				errs <- err
			}
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("concurrent legacy auth failed: %v", err)
	}

	row := storedTokenRows(t, db)[0]
	if row.Token != APITokenSecretHash(legacy) {
		t.Fatalf("row must end in digest form, got %q", row.Token)
	}
}

func TestAPITokenJSONNeverExposesSecretOrDigest(t *testing.T) {
	db, _ := newTokenHashHarness(t)
	rows := storedTokenRows(t, db)
	if len(rows) == 0 {
		// Seed one directly to serialize a realistic row.
		db, _ = newTokenHashHarness(t)
		_ = db
	}
	digest := APITokenSecretHash("abcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdefabcdef12")
	model := uModel.APIToken{ID: 7, UserID: 1, Name: "n", Token: digest}
	out, err := json.Marshal(model)
	if err != nil {
		t.Fatal(err)
	}
	blob := string(out)
	if strings.Contains(blob, digest) || strings.Contains(blob, "sha256:") || strings.Contains(blob, `"token"`) {
		t.Fatalf("serialization leaked secret/digest material: %s", blob)
	}
	_ = db
}

func TestCountLegacyPlaintextAPITokens(t *testing.T) {
	db, repo := newTokenHashHarness(t)
	owner := seedTokenOwner(t, repo)

	// Fresh state: everything hashed (or nothing stored) must report 0.
	if n, err := CountLegacyPlaintextAPITokens(context.Background(), db); err != nil || n != 0 {
		t.Fatalf("clean state must report 0 legacy rows, got %d err=%v", n, err)
	}

	legacyA := "aaaa0000bbbb1111cccc2222dddd3333eeee4444ffff55556666777788889999"
	legacyB := "bbbb0000cccc1111dddd2222eeee3333ffff444455556666777788889999aaaa"
	hashed := APITokenSecretHash("eeee0000bbbb1111cccc2222dddd3333eeee4444ffff55556666777788889999")
	for name, tok := range map[string]string{
		"legacy-a": legacyA,
		"legacy-b": legacyB,
		"hashed":   hashed,
	} {
		if err := db.Create(&uModel.APIToken{UserID: owner.ID, Name: name, Token: tok}).Error; err != nil {
			t.Fatal(err)
		}
	}
	// An empty-token row can never authenticate and is skipped by the sweep;
	// it must not be reported as pending migration work.
	if err := db.Exec("INSERT INTO api_tokens (user_id, name, token, created_at) VALUES (?, ?, '', ?)",
		owner.ID, "empty", time.Now().UTC()).Error; err != nil {
		t.Fatalf("seed empty row: %v", err)
	}

	n, err := CountLegacyPlaintextAPITokens(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if n != 2 {
		t.Fatalf("expected exactly 2 legacy plaintext rows, got %d", n)
	}

	if _, err := HashLegacyAPITokenRows(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	if n, err := CountLegacyPlaintextAPITokens(context.Background(), db); err != nil || n != 0 {
		t.Fatalf("post-sweep state must report 0, got %d err=%v", n, err)
	}

	// The counter exposes no material: exercise it through the exported
	// surface only — there is deliberately no API returning the rows.
	if _, err := repo.GetUserByToken(context.Background(), legacyA); err != nil {
		t.Fatalf("credential lost by migration: %v", err)
	}
}
