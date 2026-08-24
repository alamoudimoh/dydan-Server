package user

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func mfaSessionRepo(t *testing.T) (*UserRepository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	if err := db.AutoMigrate(&uModel.User{}, &uModel.MFASession{}); err != nil {
		t.Fatal(err)
	}
	return NewUserRepository(db, &config.Config{}), db
}

func TestReserveMFAAttemptHasBoundedConcurrentWinners(t *testing.T) {
	repo, db := mfaSessionRepo(t)
	now := time.Now().UTC()
	if err := db.Create(&uModel.MFASession{SessionToken: "challenge", UserID: 1, AuthMethod: "password", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}).Error; err != nil {
		t.Fatal(err)
	}

	const maxAttempts = 5
	var wg sync.WaitGroup
	winners := make(chan bool, 12)
	for range 12 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, recorded, err := repo.ReserveMFAAttempt(context.Background(), "challenge", maxAttempts)
			if err != nil {
				t.Errorf("record attempt: %v", err)
			}
			winners <- recorded
		}()
	}
	wg.Wait()
	close(winners)
	recorded := 0
	for won := range winners {
		if won {
			recorded++
		}
	}
	if recorded != maxAttempts {
		t.Fatalf("recorded attempts = %d, want %d", recorded, maxAttempts)
	}
	var session uModel.MFASession
	db.Where("session_token = ?", "challenge").First(&session)
	if session.Attempts != maxAttempts {
		t.Fatalf("stored attempts = %d, want %d", session.Attempts, maxAttempts)
	}
}

func TestBackupCodeCanOnlyWinOneConcurrentMFASession(t *testing.T) {
	repo, db := mfaSessionRepo(t)
	now := time.Now().UTC()
	user := &uModel.User{ID: 1, Username: "alice", Email: "alice@example.com", MFARecoveryUsed: "[]"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	for _, token := range []string{"challenge-a", "challenge-b"} {
		if err := db.Create(&uModel.MFASession{SessionToken: token, UserID: user.ID, AuthMethod: "password", CreatedAt: now, ExpiresAt: now.Add(time.Minute)}).Error; err != nil {
			t.Fatal(err)
		}
	}

	var wg sync.WaitGroup
	winners := make(chan bool, 2)
	for _, token := range []string{"challenge-a", "challenge-b"} {
		wg.Add(1)
		go func(token string) {
			defer wg.Done()
			won, err := repo.ConsumeMFASession(context.Background(), token, user.ID, "[]", `["backup-code"]`)
			if err != nil {
				t.Errorf("consume MFA session: %v", err)
			}
			winners <- won
		}(token)
	}
	wg.Wait()
	close(winners)
	count := 0
	for won := range winners {
		if won {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("backup-code winners = %d, want 1", count)
	}
}
