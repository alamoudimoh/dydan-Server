package resource

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newDBHealthHarness(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(
		filepath.ToSlash(filepath.Join(t.TempDir(), "health.db"))+"?_pragma=busy_timeout(5000)"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.APIToken{}, &cModel.Circle{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	r := gin.New()
	r.GET("/api/v1/health", NewHandler(&config.Config{}, db).getDBHealth)
	return r, db
}

// TestDBHealthReportsLegacyPlaintextMigrationState pins the operator-facing
// observability contract for the API-token plaintext migration: the endpoint
// distinguishes a fully drained installation from one with pending legacy
// rows, using counts/status only.
func TestDBHealthReportsLegacyPlaintextMigrationState(t *testing.T) {
	r, db := newDBHealthHarness(t)
	repo := uRepo.NewUserRepository(db, &config.Config{})

	owner, err := repo.CreateUser(context.Background(), &uModel.User{
		Username: "health-owner", Email: "health-owner@example.com",
	})
	if err != nil {
		t.Fatal(err)
	}

	get := func() (int, string) {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/health", nil)
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	code, body := get()
	if code != http.StatusOK {
		t.Fatalf("healthy instance must report 200, got %d", code)
	}
	var clean struct {
		Status            string `json:"status"`
		APITokenMigration struct {
			State               string `json:"state"`
			LegacyPlaintextRows *int   `json:"legacy_plaintext_rows"`
		} `json:"api_token_migration"`
	}
	if err := json.Unmarshal([]byte(body), &clean); err != nil {
		t.Fatal(err)
	}
	if clean.Status != "ok" || clean.APITokenMigration.State != "complete" {
		t.Fatalf("expected complete/clean migration state, got %s", body)
	}

	// Seed two pre-migration plaintext rows plus one hashed row.
	legacyA := "aaaa0000bbbb1111cccc2222dddd3333eeee4444ffff55556666777788889999"
	legacyB := "bbbb0000cccc1111dddd2222eeee3333ffff444455556666777788889999aaaa"
	hashed := uRepo.APITokenSecretHash("eeee0000bbbb1111cccc2222dddd3333eeee4444ffff55556666777788889999")
	for name, tok := range map[string]string{"l-a": legacyA, "l-b": legacyB, "h": hashed} {
		if err := db.Create(&uModel.APIToken{UserID: owner.ID, Name: name, Token: tok, CreatedAt: time.Now().UTC()}).Error; err != nil {
			t.Fatal(err)
		}
	}

	code, body = get()
	if code != http.StatusOK {
		t.Fatalf("pending migration must NOT fail the health probe (no restart loops), got %d", code)
	}
	var pending struct {
		Status            string `json:"status"`
		APITokenMigration struct {
			State               string `json:"state"`
			LegacyPlaintextRows int    `json:"legacy_plaintext_rows"`
		} `json:"api_token_migration"`
	}
	if err := json.Unmarshal([]byte(body), &pending); err != nil {
		t.Fatal(err)
	}
	if pending.Status != "ok" {
		t.Fatalf("pending sweep work must not mark the instance unhealthy")
	}
	if pending.APITokenMigration.State != "pending" || pending.APITokenMigration.LegacyPlaintextRows != 2 {
		t.Fatalf("expected pending state with exactly 2 legacy rows, got %s", body)
	}

	// No credential material of any kind may appear in the response.
	for _, secret := range []string{legacyA, legacyB,
		"eeee0000bbbb1111cccc2222dddd3333eeee4444ffff55556666777788889999",
		hashed, "sha256:"} {
		if strings.Contains(body, secret) {
			t.Fatalf("health response leaked token material: %q present in %s", secret, body)
		}
	}

	if _, err := uRepo.HashLegacyAPITokenRows(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	_, body = get()
	if !strings.Contains(body, `"complete"`) {
		t.Fatalf("post-sweep health must report complete, got %s", body)
	}
}

// TestDBHealthUnhealthyOnDatabaseFailure pins that DB failures still surface
// as 503 with no internal detail.
func TestDBHealthUnhealthyOnClosedDatabase(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.ToSlash(filepath.Join(t.TempDir(), "closed.db"))), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, _ := db.DB()
	_ = sqlDB.Close()

	r := gin.New()
	r.GET("/api/v1/health", NewHandler(&config.Config{}, db).getDBHealth)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected 503 on dead database, got %d", w.Code)
	}
}
