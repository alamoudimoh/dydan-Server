package user

import (
	"context"
	cryptorand "crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	cModel "github.com/alamoudimoh/daydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// postgresTestDB skips unless DAYDAN_TEST_POSTGRES_DSN is set, e.g.
//
//	DAYDAN_TEST_POSTGRES_DSN="host=localhost port=54329 user=daydan \
//	  password=daydan dbname=daydan sslmode=disable" go test ./internal/user/repo/ -run Postgres
//
// It provides live PostgreSQL coverage for the duplicate-signup constraint
// classification and the hashed API-token storage path.
func postgresTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("DAYDAN_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("DAYDAN_TEST_POSTGRES_DSN not set; skipping live PostgreSQL coverage")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.APIToken{}, &uModel.UserNotificationTarget{}, &cModel.Circle{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestPostgresDuplicateSignupClassifiedAsConflict(t *testing.T) {
	db := postgresTestDB(t)
	repo := NewUserRepository(db, config4Test())

	suffix := "_" + randomSuffix(t)
	if _, err := repo.CreateUser(context.Background(), &uModel.User{
		Username: "pgdup" + suffix, Email: "pgdup" + suffix + "@example.com",
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	// Direct insert past the pre-check must produce a native pgconn 23505,
	// classified as the duplicate sentinel.
	rawErr := db.Create(&uModel.User{Username: "pgdup" + suffix, Email: "other" + suffix + "@example.com"}).Error
	if rawErr == nil {
		t.Fatal("expected unique violation")
	}
	var pgErr interface{ SQLState() string }
	if errors.As(rawErr, &pgErr) == false && !classifyDuplicateIdentity(rawErr) {
		t.Fatalf("expected classifiable pg error, got %T: %v", rawErr, rawErr)
	}
	if !classifyDuplicateIdentity(rawErr) {
		t.Fatalf("postgres username violation must classify as duplicate: %v", rawErr)
	}

	if _, err := repo.CreateUser(context.Background(), &uModel.User{
		Username: "pgother" + suffix, Email: "pgdup" + suffix + "@example.com",
	}); !errors.Is(err, ErrUserAlreadyExists) {
		t.Fatalf("CreateUser must return ErrUserAlreadyExists via constraint path, got %v", err)
	}
}

func TestPostgresHashedAPITokenLifecycle(t *testing.T) {
	db := postgresTestDB(t)
	repo := NewUserRepository(db, config4Test())

	suffix := "_" + randomSuffix(t)
	owner, err := repo.CreateUser(context.Background(), &uModel.User{
		Username: "pgtok" + suffix, Email: "pgtok" + suffix + "@example.com",
	})
	if err != nil {
		t.Fatalf("seed owner: %v", err)
	}

	// Unique per run: the container database persists across invocations.
	buf := make([]byte, 32)
	if _, err := cryptorand.Read(buf); err != nil {
		t.Fatal(err)
	}
	secret := hex.EncodeToString(buf)
	stored, err := repo.StoreAPIToken(context.Background(), owner.ID, "pg", secret)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	var row uModel.APIToken
	if err := db.First(&row, stored.ID).Error; err != nil {
		t.Fatal(err)
	}
	if row.Token == secret || len(row.Token) < 8 || row.Token[:7] != "sha256:" {
		t.Fatalf("postgres row must hold prefixed digest, got %q", row.Token)
	}

	user, err := repo.GetUserByToken(context.Background(), secret)
	if err != nil {
		t.Fatalf("auth on postgres failed: %v", err)
	}
	if user.ID != owner.ID {
		t.Fatalf("wrong user authenticated: %d", user.ID)
	}
}

func TestPostgresConcurrentSignupSingleWinner(t *testing.T) {
	db := postgresTestDB(t)
	repo := NewUserRepository(db, config4Test())

	const workers = 8
	username := "pgrace" + randomSuffix(t)
	start := make(chan struct{})
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			<-start
			_, err := repo.CreateUser(context.Background(), &uModel.User{
				Username: username,
				Email:    username + "-" + string(rune('a'+n)) + "@example.com",
			})
			errs <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(errs)

	successes := 0
	for err := range errs {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, ErrUserAlreadyExists):
		default:
			t.Fatalf("unexpected loser error on postgres: %v", err)
		}
	}
	if successes != 1 {
		t.Fatalf("exactly one winner expected, got %d", successes)
	}
	var count int64
	db.Model(&uModel.User{}).Where("username = ?", username).Count(&count)
	if count != 1 {
		t.Fatalf("expected exactly one row, got %d", count)
	}
}

func config4Test() *config.Config {
	cfg := &config.Config{}
	cfg.Jwt.SessionTime = time.Hour
	return cfg
}

var suffixCounter atomic.Int64

func randomSuffix(t *testing.T) string {
	t.Helper()
	return strconv.FormatInt(suffixCounter.Add(1), 10) + "x" + strconv.FormatInt(time.Now().UnixNano()%100000, 10)
}

func TestPostgresLegacyTokenMigrationSweep(t *testing.T) {
	db := postgresTestDB(t)
	repo := NewUserRepository(db, config4Test())

	suffix := randomSuffix(t) + "m"
	owner, err := repo.CreateUser(context.Background(), &uModel.User{
		Username: "pglegacy" + suffix, Email: "pglegacy" + suffix + "@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	buf := make([]byte, 32)
	if _, err := cryptorand.Read(buf); err != nil {
		t.Fatal(err)
	}
	legacy := hex.EncodeToString(buf)
	if err := db.Create(&uModel.APIToken{UserID: owner.ID, Name: "pg-legacy-" + suffix, Token: legacy}).Error; err != nil {
		t.Fatal(err)
	}

	n, err := HashLegacyAPITokenRows(context.Background(), db)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n < 1 {
		t.Fatalf("sweep must convert the seeded legacy row, converted %d", n)
	}

	var row uModel.APIToken
	if err := db.Where("name = ?", "pg-legacy-"+suffix).First(&row).Error; err != nil {
		t.Fatal(err)
	}
	if row.Token != APITokenSecretHash(legacy) {
		t.Fatalf("postgres row not upgraded to digest: %q", row.Token)
	}
	if _, err := repo.GetUserByToken(context.Background(), legacy); err != nil {
		t.Fatalf("legacy credential broken after sweep: %v", err)
	}
}
