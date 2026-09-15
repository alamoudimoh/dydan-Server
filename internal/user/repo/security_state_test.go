package user

import (
	"context"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func securityStateRepo(t *testing.T) (*UserRepository, *gorm.DB, *uModel.User) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserSession{}); err != nil {
		t.Fatal(err)
	}
	user := &uModel.User{Username: "security-state"}
	if err := db.Create(user).Error; err != nil {
		t.Fatal(err)
	}
	return NewUserRepository(db, &config.Config{}), db, user
}

func addLiveSession(t *testing.T, db *gorm.DB, userID int, id string) {
	t.Helper()
	if err := db.Create(&uModel.UserSession{
		ID: id, UserID: userID, TokenHash: "hash-" + id, FamilyID: "family-" + id,
		CreatedAt: time.Now().UTC(), ExpiresAt: time.Now().UTC().Add(time.Hour),
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func assertSecurityState(t *testing.T, db *gorm.DB, userID, wantVersion int, wantLive int64) {
	t.Helper()
	var user uModel.User
	if err := db.First(&user, userID).Error; err != nil {
		t.Fatal(err)
	}
	if user.CredentialVersion != wantVersion {
		t.Fatalf("credential version=%d want=%d", user.CredentialVersion, wantVersion)
	}
	var live int64
	if err := db.Model(&uModel.UserSession{}).Where("user_id = ? AND revoked_at IS NULL", userID).Count(&live).Error; err != nil {
		t.Fatal(err)
	}
	if live != wantLive {
		t.Fatalf("live sessions=%d want=%d", live, wantLive)
	}
}

func TestChildPasswordChangeAtomicallyAdvancesSecurityState(t *testing.T) {
	repo, db, user := securityStateRepo(t)
	addLiveSession(t, db, user.ID, "child-session")
	if err := repo.ChangeChildPasswordAndRevokeSessions(context.Background(), user.ID, "new-hash"); err != nil {
		t.Fatal(err)
	}
	assertSecurityState(t, db, user.ID, 1, 0)
}

func TestMFAChangesAdvanceSecurityStateAndRevokeAllFamilies(t *testing.T) {
	repo, db, user := securityStateRepo(t)
	addLiveSession(t, db, user.ID, "before-enable")
	if err := repo.EnableMFA(context.Background(), user.ID, "secret", []string{"backup"}); err != nil {
		t.Fatal(err)
	}
	assertSecurityState(t, db, user.ID, 1, 0)
	addLiveSession(t, db, user.ID, "before-disable")
	if err := repo.DisableMFA(context.Background(), user.ID); err != nil {
		t.Fatal(err)
	}
	assertSecurityState(t, db, user.ID, 2, 0)
}
