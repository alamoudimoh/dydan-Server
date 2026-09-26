package user

import (
	"context"
	"errors"
	"testing"
	"time"

	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
)

func TestEmailChangeTransactionIsHashedSingleUseAndRevokesSessions(t *testing.T) {
	r, db := passwordResetRepo(t)
	ctx := context.Background()
	user := &uModel.User{ID: 1, Username: "alice", Email: "alice@example.test", Password: "old"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&uModel.UserSession{ID: "session", UserID: user.ID, TokenHash: "hash", FamilyID: "family", ExpiresAt: time.Now().Add(time.Hour)}).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.CreateEmailChangeTransaction(ctx, user.ID, " New@Example.Test ", "change-secret"); err != nil {
		t.Fatal(err)
	}
	var change uModel.EmailChangeTransaction
	if err := db.First(&change).Error; err != nil {
		t.Fatal(err)
	}
	if change.Email != "new@example.test" || change.TokenHash == "change-secret" || change.TokenHash != emailChangeTokenHash("change-secret") {
		t.Fatalf("unsafe or uncanonical transaction: %+v", change)
	}
	if err := r.ConsumeEmailChangeTransaction(ctx, "change-secret"); err != nil {
		t.Fatal(err)
	}
	if err := r.ConsumeEmailChangeTransaction(ctx, "change-secret"); !errors.Is(err, ErrInvalidEmailChangeToken) {
		t.Fatalf("replayed transaction = %v", err)
	}
	var updated uModel.User
	if err := db.First(&updated, user.ID).Error; err != nil {
		t.Fatal(err)
	}
	if updated.Email != "new@example.test" || updated.CredentialVersion != 1 {
		t.Fatalf("email/security state not updated: %+v", updated)
	}
	var live int64
	db.Model(&uModel.UserSession{}).Where("user_id = ? AND revoked_at IS NULL", user.ID).Count(&live)
	if live != 0 {
		t.Fatalf("email change left %d live sessions", live)
	}
}

func TestEmailChangeTransactionRejectsExpiryDuplicateAndWrongToken(t *testing.T) {
	r, db := passwordResetRepo(t)
	ctx := context.Background()
	if err := db.Create(&uModel.User{ID: 1, Username: "alice", Email: "alice@example.test"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&uModel.User{ID: 2, Username: "bob", Email: "taken@example.test"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.CreateEmailChangeTransaction(ctx, 1, "taken@example.test", "duplicate"); err != nil {
		t.Fatal(err)
	}
	if err := r.ConsumeEmailChangeTransaction(ctx, "tampered"); !errors.Is(err, ErrInvalidEmailChangeToken) {
		t.Fatalf("tampered token = %v", err)
	}
	if err := r.ConsumeEmailChangeTransaction(ctx, "duplicate"); !errors.Is(err, ErrEmailAlreadyInUse) {
		t.Fatalf("duplicate destination = %v", err)
	}
	if err := r.CreateEmailChangeTransaction(ctx, 1, "fresh@example.test", "expired"); err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&uModel.EmailChangeTransaction{}).Where("token_hash = ?", emailChangeTokenHash("expired")).Update("expires_at", time.Now().UTC().Add(-time.Minute)).Error; err != nil {
		t.Fatal(err)
	}
	if err := r.ConsumeEmailChangeTransaction(ctx, "expired"); !errors.Is(err, ErrInvalidEmailChangeToken) {
		t.Fatalf("expired token = %v", err)
	}
	var original uModel.User
	db.First(&original, 1)
	if original.Email != "alice@example.test" {
		t.Fatalf("email changed before a valid verification: %s", original.Email)
	}
}
