package user

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func passwordResetRepo(t *testing.T) (*UserRepository, *gorm.DB) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared&_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&cModel.Circle{}, &uModel.User{}, &uModel.UserPasswordReset{}, &uModel.UserSession{}, &uModel.MFASession{}); err != nil {
		t.Fatal(err)
	}
	return NewUserRepository(db, &config.Config{IsDydanCloud: false}), db
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
	if err := db.Create(&uModel.MFASession{SessionToken: "pending-mfa", UserID: user.ID, AuthMethod: "password", CreatedAt: now, ExpiresAt: now.Add(time.Hour)}).Error; err != nil {
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
	var mfaRows int64
	db.Model(&uModel.MFASession{}).Where("user_id = ?", user.ID).Count(&mfaRows)
	if mfaRows != 0 {
		t.Fatal("password reset left a pending MFA login alive")
	}
	var updated uModel.User
	db.First(&updated, user.ID)
	if updated.CredentialVersion != 1 {
		t.Fatalf("credential version = %d, want 1", updated.CredentialVersion)
	}
}

func TestSetPasswordResetTokenStoresHashAndSupersedesPriorLink(t *testing.T) {
	r, db := passwordResetRepo(t)
	ctx := context.Background()
	user := &uModel.User{ID: 1, Username: "alice", Email: "alice@example.com", Password: "old"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.SetPasswordResetToken(ctx, user.Email, "first-secret"); err != nil {
		t.Fatal(err)
	}
	if err := r.SetPasswordResetToken(ctx, user.Email, "second-secret"); err != nil {
		t.Fatal(err)
	}

	var rows []uModel.UserPasswordReset
	if err := db.Find(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("reset rows = %d, want 1", len(rows))
	}
	if rows[0].Token == "second-secret" || rows[0].Token != passwordResetTokenHash("second-secret") {
		t.Fatal("new reset credential was not hashed at rest")
	}
	if rows[0].ExpirationDate.After(rows[0].CreatedAt.Add(passwordResetLifetime + time.Second)) {
		t.Fatal("reset credential lifetime exceeds policy")
	}
	if err := r.UpdatePasswordByToken(ctx, user.Email, "first-secret", "nope"); !errors.Is(err, ErrInvalidPasswordResetToken) {
		t.Fatalf("superseded link should be invalid: %v", err)
	}
}

func TestCleanupExpiredPasswordResets(t *testing.T) {
	r, db := passwordResetRepo(t)
	now := time.Now().UTC()
	rows := []uModel.UserPasswordReset{
		{Token: "expired", ExpirationDate: now.Add(-time.Minute)},
		{Token: "live", ExpirationDate: now.Add(time.Minute)},
	}
	if err := db.Create(&rows).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.CleanupExpiredPasswordResets(context.Background()); err != nil {
		t.Fatal(err)
	}
	var count int64
	db.Model(&uModel.UserPasswordReset{}).Count(&count)
	if count != 1 {
		t.Fatalf("reset rows after cleanup = %d, want 1", count)
	}
}

func TestPasswordResetSchemaUpgradesLegacyTable(t *testing.T) {
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE user_password_resets (
		user_id integer,
		email text,
		token text PRIMARY KEY,
		expiration_date datetime
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO user_password_resets (user_id, email, token, expiration_date) VALUES (?, ?, ?, ?)", 1, "legacy@example.com", "legacy-token", time.Now().UTC().Add(time.Hour)).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.UserPasswordReset{}); err != nil {
		t.Fatal(err)
	}
	if !db.Migrator().HasColumn(&uModel.UserPasswordReset{}, "created_at") {
		t.Fatal("legacy reset table was not upgraded with created_at")
	}
	var row uModel.UserPasswordReset
	if err := db.Where("token = ?", "legacy-token").First(&row).Error; err != nil {
		t.Fatalf("legacy reset row was not preserved: %v", err)
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
