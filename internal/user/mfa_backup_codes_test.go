package user

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	auth "github.com/alamoudimoh/dydan-server/internal/auth"
	"github.com/alamoudimoh/dydan-server/internal/mfa"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/pquerna/otp/totp"
	"gorm.io/gorm"
)

func TestRegenerateBackupCodesRequiresMFAAndRotatesCodes(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserSession{}); err != nil {
		t.Fatalf("migrate user: %v", err)
	}
	cfg := &config.Config{}
	service := mfa.NewService(cfg)
	repo := uRepo.NewUserRepository(db, cfg)
	key, err := service.GenerateSecret("mfa@example.test")
	if err != nil {
		t.Fatalf("generate secret: %v", err)
	}
	user := &uModel.User{Username: "mfa-user", Email: "mfa@example.test", MFAEnabled: true, MFASecret: key.Secret(), MFABackupCodes: `["OLD-CODE"]`}
	if err := db.Create(user).Error; err != nil {
		t.Fatalf("seed user: %v", err)
	}
	h := &Handler{userRepo: repo, mfaService: service}
	r := gin.New()
	r.Use(func(c *gin.Context) {
		auth.SetCurrentUser(c, &uModel.UserDetails{User: *user})
		c.Next()
	})
	r.POST("/api/v1/users/mfa/regenerate-backup-codes", h.regenerateBackupCodes)

	code, err := totp.GenerateCode(key.Secret(), time.Now())
	if err != nil {
		t.Fatalf("generate totp: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/users/mfa/regenerate-backup-codes", strings.NewReader(`{"code":"`+code+`"}`))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("regenerate backup codes: got %d (%s)", w.Code, w.Body.String())
	}
	var body struct {
		BackupCodes []string `json:"backupCodes"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(body.BackupCodes) != 8 {
		t.Fatalf("expected 8 replacement codes, got %d", len(body.BackupCodes))
	}
	var stored uModel.User
	if err := db.First(&stored, user.ID).Error; err != nil {
		t.Fatalf("load user: %v", err)
	}
	if stored.MFABackupCodes == user.MFABackupCodes {
		t.Fatal("backup codes were not rotated")
	}
}
