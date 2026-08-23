package user

import (
	"context"
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
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserPasswordReset{}); err != nil {
		t.Fatal(err)
	}
	return NewUserRepository(db, &config.Config{IsDayDanCloud: false}), db
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
