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

func passwordResetRepo(t *testing.T) (*UserRepository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserPasswordReset{}, &uModel.UserSession{}); err != nil {
		t.Fatal(err)
	}
	return NewUserRepository(db, &config.Config{IsDayDanCloud: false}), db
}

func TestUpdatePasswordByTokenRevokesAllRefreshSessions(t *testing.T) {
	r, db := passwordResetRepo(t)
	ctx := context.Background()
	now := time.Now().UTC()
	user := &uModel.User{ID: 1, Username: "alice", Email: "alice@example.com", Password: "old"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&uModel.UserPasswordReset{UserID: user.ID, Token: "valid-token", Email: user.Email, ExpirationDate: now.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"session-a", "session-b"} {
		if err := db.Create(&uModel.UserSession{ID: id, UserID: user.ID, TokenHash: "hash-" + id, FamilyID: "family-" + id, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}).Error; err != nil {
			t.Fatal(err)
		}
	}

	if err := r.UpdatePasswordByToken(ctx, user.Email, "valid-token", "newpass"); err != nil {
		t.Fatalf("reset password: %v", err)
	}
	var live int64
	db.Model(&uModel.UserSession{}).Where("user_id = ? AND revoked_at IS NULL", user.ID).Count(&live)
	if live != 0 {
		t.Fatalf("password reset left %d live refresh sessions", live)
	}
	var resetRows int64
	db.Model(&uModel.UserPasswordReset{}).Where("token = ?", "valid-token").Count(&resetRows)
	if resetRows != 0 {
		t.Fatal("reset token was not consumed transactionally")
	}
}

func TestUpdatePasswordByTokenHasSingleConcurrentWinner(t *testing.T) {
	r, db := passwordResetRepo(t)
	now := time.Now().UTC()
	user := &uModel.User{ID: 1, Username: "alice", Email: "alice@example.com", Password: "old"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&uModel.UserPasswordReset{UserID: user.ID, Token: "one-use", Email: user.Email, ExpirationDate: now.Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	results := make(chan error, 2)
	for _, password := range []string{"first", "second"} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- r.UpdatePasswordByToken(context.Background(), user.Email, "one-use", password)
		}()
	}
	wg.Wait()
	close(results)
	winners := 0
	for err := range results {
		if err == nil {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("expected one reset-token winner, got %d", winners)
	}
}

// Regression: UpdatePasswordByToken must reject reset tokens whose expiration
// date has passed. Previously the token row was matched without checking the
// expiration, leaving leaked reset links valid forever.
func TestUpdatePasswordByTokenRejectsExpiredToken(t *testing.T) {
	r, db := passwordResetRepo(t)
	ctx := context.Background()

	if err := db.Create(&uModel.User{ID: 1, Username: "alice", Email: "alice@example.com", Password: "old"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&uModel.UserPasswordReset{
		UserID:         1,
		Token:          "expired-token",
		Email:          "alice@example.com",
		ExpirationDate: time.Now().UTC().Add(-time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&uModel.UserPasswordReset{
		UserID:         1,
		Token:          "valid-token",
		Email:          "alice@example.com",
		ExpirationDate: time.Now().UTC().Add(24 * time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}

	if err := r.UpdatePasswordByToken(ctx, "alice@example.com", "expired-token", "newpass"); err == nil {
		t.Fatal("expected expired token to be rejected")
	}
	var count int64
	db.Model(&uModel.UserPasswordReset{}).Where("token = ?", "expired-token").Count(&count)
	if count != 1 {
		t.Fatalf("expired token row should remain untouched, got %d rows", count)
	}

	if err := r.UpdatePasswordByToken(ctx, "alice@example.com", "valid-token", "newpass"); err != nil {
		t.Fatalf("expected valid token to be accepted: %v", err)
	}
}
