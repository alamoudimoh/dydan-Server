package user

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	auth "github.com/alamoudimoh/dydan-server/internal/auth"
	chModel "github.com/alamoudimoh/dydan-server/internal/chore/model"
	cModel "github.com/alamoudimoh/dydan-server/internal/circle/model"
	cRepo "github.com/alamoudimoh/dydan-server/internal/circle/repo"
	"github.com/alamoudimoh/dydan-server/internal/email"
	mModel "github.com/alamoudimoh/dydan-server/internal/meter/model"
	nModel "github.com/alamoudimoh/dydan-server/internal/notifier/model"
	tModel "github.com/alamoudimoh/dydan-server/internal/thing/model"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	uRepo "github.com/alamoudimoh/dydan-server/internal/user/repo"
	"github.com/alamoudimoh/dydan-server/logging"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gorm.io/gorm"
)

type emailChangeSender struct {
	token string
	err   error
}

func (*emailChangeSender) SendResetPasswordEmail(context.Context, string, string) error { return nil }
func (*emailChangeSender) SendVerificationEmail(string, string) error                   { return nil }
func (*emailChangeSender) SendTestEmail(context.Context, string) error                  { return nil }
func (s *emailChangeSender) SendEmailChangeVerification(_ context.Context, _ string, token string) error {
	s.token = token
	return s.err
}

func emailChangeHandler(t *testing.T, sender email.Sender) (*Handler, *gorm.DB, *uModel.User) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &uModel.UserSession{}, &uModel.UserPreference{}, &uModel.EmailChangeTransaction{}, &uModel.UserErrorReport{}); err != nil {
		t.Fatal(err)
	}
	password, err := auth.EncodePassword("correct-password")
	if err != nil {
		t.Fatal(err)
	}
	account := &uModel.User{ID: 1, CircleID: 17, Username: "alice", Email: "alice@example.test", Password: password}
	if err := db.Create(account).Error; err != nil {
		t.Fatal(err)
	}
	return &Handler{userRepo: uRepo.NewUserRepository(db, &config.Config{}), email: sender}, db, account
}

func TestReportProblemRequiresAuthenticationAndPersistsTrustedOwnership(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db, account := emailChangeHandler(t, &emailChangeSender{})

	unauthorized := callEmailChangeHandler(t, h, nil, "/error-reports", map[string]string{"summary": "failed"}, h.reportProblem)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d", unauthorized.Code)
	}
	invalid := callEmailChangeHandler(t, h, account, "/error-reports", map[string]string{"details": "no summary"}, h.reportProblem)
	if invalid.Code != http.StatusBadRequest {
		t.Fatalf("invalid status = %d", invalid.Code)
	}

	created := callEmailChangeHandler(t, h, account, "/error-reports", map[string]string{
		"summary": "Task save failed",
		"details": "No credentials, tokens, or message content are included.",
		"screen":  "error",
	}, h.reportProblem)
	if created.Code != http.StatusCreated {
		t.Fatalf("created status = %d: %s", created.Code, created.Body.String())
	}
	var reports []uModel.UserErrorReport
	if err := db.Find(&reports).Error; err != nil {
		t.Fatal(err)
	}
	if len(reports) != 1 {
		t.Fatalf("reports = %+v", reports)
	}
	report := reports[0]
	if report.UserID != account.ID || report.CircleID != account.CircleID {
		t.Fatalf("report ownership was not stamped from the authenticated account: %+v", report)
	}
	if report.Summary != "Task save failed" || report.Screen != "error" {
		t.Fatalf("report body was not persisted: %+v", report)
	}
}

func callEmailChangeHandler(t *testing.T, h *Handler, account *uModel.User, endpoint string, body any, fn gin.HandlerFunc) *httptest.ResponseRecorder {
	t.Helper()
	payload, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, endpoint, bytes.NewReader(payload))
	ctx.Request.Header.Set("Content-Type", "application/json")
	if account != nil {
		auth.SetCurrentUser(ctx, &uModel.UserDetails{User: *account})
	}
	fn(ctx)
	return recorder
}

func TestEmailChangeHandlerRequiresReauthenticationAndVerification(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sender := &emailChangeSender{}
	h, db, account := emailChangeHandler(t, sender)

	wrongPassword := callEmailChangeHandler(t, h, account, "/email-change", map[string]string{"email": "new@example.test", "currentPassword": "wrong"}, h.startEmailChange)
	if wrongPassword.Code != http.StatusForbidden {
		t.Fatalf("wrong password status = %d", wrongPassword.Code)
	}
	start := callEmailChangeHandler(t, h, account, "/email-change", map[string]string{"email": " New@Example.Test ", "currentPassword": "correct-password"}, h.startEmailChange)
	if start.Code != http.StatusAccepted || sender.token == "" {
		t.Fatalf("start = %d, token=%q", start.Code, sender.token)
	}
	var before uModel.User
	db.First(&before, account.ID)
	if before.Email != "alice@example.test" {
		t.Fatalf("email changed before verification: %s", before.Email)
	}
	verified := callEmailChangeHandler(t, h, nil, "/email-change/verify", map[string]string{"token": sender.token}, h.verifyEmailChange)
	if verified.Code != http.StatusOK {
		t.Fatalf("verification = %d: %s", verified.Code, verified.Body.String())
	}
	var after uModel.User
	db.First(&after, account.ID)
	if after.Email != "new@example.test" {
		t.Fatalf("verified email = %s", after.Email)
	}
	replay := callEmailChangeHandler(t, h, nil, "/email-change/verify", map[string]string{"token": sender.token}, h.verifyEmailChange)
	if replay.Code != http.StatusBadRequest {
		t.Fatalf("replay = %d", replay.Code)
	}
}

func TestEmailChangeRejectsExistingDestinationBeforeSending(t *testing.T) {
	gin.SetMode(gin.TestMode)
	sender := &emailChangeSender{}
	h, db, account := emailChangeHandler(t, sender)
	if err := db.Create(&uModel.User{ID: 2, Username: "taken", Email: "taken@example.test"}).Error; err != nil {
		t.Fatal(err)
	}

	response := callEmailChangeHandler(t, h, account, "/email-change", map[string]string{
		"email": " Taken@Example.Test ", "currentPassword": "correct-password",
	}, h.startEmailChange)
	if response.Code != http.StatusConflict {
		t.Fatalf("duplicate destination status = %d: %s", response.Code, response.Body.String())
	}
	if sender.token != "" {
		t.Fatal("duplicate destination sent a verification link")
	}
	var count int64
	if err := db.Model(&uModel.EmailChangeTransaction{}).Count(&count).Error; err != nil || count != 0 {
		t.Fatalf("duplicate destination persisted a transaction: count=%d err=%v", count, err)
	}
}

func TestEmailChangeHandlerDeletesUndeliveredTransaction(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db, account := emailChangeHandler(t, &emailChangeSender{err: errors.New("SMTP down")})
	response := callEmailChangeHandler(t, h, account, "/email-change", map[string]string{"email": "new@example.test", "currentPassword": "correct-password"}, h.startEmailChange)
	if response.Code != http.StatusServiceUnavailable {
		t.Fatalf("SMTP failure status = %d", response.Code)
	}
	var count int64
	db.Model(&uModel.EmailChangeTransaction{}).Count(&count)
	if count != 0 {
		t.Fatalf("undelivered verification left %d transaction(s)", count)
	}
}

func TestExportMyDataRequiresAuthenticationAndExcludesSecurityMaterial(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db, account := emailChangeHandler(t, &emailChangeSender{})
	if err := db.Create(&uModel.UserPreference{UserID: account.ID, Value: `{"theme":"dydan"}`}).Error; err != nil {
		t.Fatal(err)
	}
	unauthorized := callEmailChangeHandler(t, h, nil, "/export", nil, h.exportMyData)
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized export = %d", unauthorized.Code)
	}
	exported := callEmailChangeHandler(t, h, account, "/export", nil, h.exportMyData)
	if exported.Code != http.StatusOK {
		t.Fatalf("export = %d: %s", exported.Code, exported.Body.String())
	}
	body := exported.Body.String()
	for _, secret := range []string{"correct-password", "credentialVersion", "mfa", "tokenHash", "refresh"} {
		if bytes.Contains([]byte(body), []byte(secret)) {
			t.Fatalf("export exposed security material %q: %s", secret, body)
		}
	}
	if !bytes.Contains([]byte(body), []byte(`"format":"dydan-user-export/v1"`)) || !bytes.Contains([]byte(body), []byte(`"theme":"dydan"`)) {
		t.Fatalf("export lacks portable account content: %s", body)
	}
	if exported.Header().Get("Cache-Control") != "no-store, private" {
		t.Fatalf("missing no-store export policy: %q", exported.Header().Get("Cache-Control"))
	}
}

func TestExportMyDataIncludesAuthorizedApplicationDataOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db, account := emailChangeHandler(t, &emailChangeSender{})
	if err := db.AutoMigrate(
		&chModel.Chore{}, &chModel.ChoreHistory{}, &mModel.Meter{}, &mModel.Reading{},
		&tModel.Thing{}, &tModel.ThingHistory{}, &nModel.InboxNotification{},
	); err != nil {
		t.Fatal(err)
	}
	owned := chModel.Chore{ID: 101, Name: "My current-circle task", CircleID: account.CircleID, CreatedBy: account.ID, IsActive: true}
	foreign := chModel.Chore{ID: 102, Name: "Foreign circle task", CircleID: account.CircleID + 1, CreatedBy: account.ID, IsActive: true}
	if err := db.Create(&[]chModel.Chore{owned, foreign}).Error; err != nil {
		t.Fatal(err)
	}
	meter := mModel.Meter{ID: 201, CircleID: account.CircleID, CreatedBy: account.ID, Name: "My meter", Unit: mModel.UnitCount}
	foreignMeter := mModel.Meter{ID: 202, CircleID: account.CircleID + 1, CreatedBy: account.ID, Name: "Foreign meter", Unit: mModel.UnitCount}
	if err := db.Create(&[]mModel.Meter{meter, foreignMeter}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]mModel.Reading{{MeterID: meter.ID, Reading: 3, RecordedBy: account.ID}, {MeterID: foreignMeter.ID, Reading: 99, RecordedBy: account.ID}}).Error; err != nil {
		t.Fatal(err)
	}
	thing := tModel.Thing{ID: 301, UserID: account.ID, CircleID: account.CircleID, Name: "My thing", Type: "boolean", State: "off"}
	foreignThing := tModel.Thing{ID: 302, UserID: account.ID, CircleID: account.CircleID + 1, Name: "Foreign thing", Type: "boolean", State: "off"}
	if err := db.Create(&[]tModel.Thing{thing, foreignThing}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]tModel.ThingHistory{{ThingID: thing.ID, State: "on"}, {ThingID: foreignThing.ID, State: "on"}}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]nModel.InboxNotification{{CircleID: account.CircleID, UserID: account.ID, Type: "task", Severity: "info", Title: "Mine", DedupKey: "mine"}, {CircleID: account.CircleID + 1, UserID: account.ID, Type: "task", Severity: "info", Title: "Foreign", DedupKey: "foreign"}}).Error; err != nil {
		t.Fatal(err)
	}

	exported := callEmailChangeHandler(t, h, account, "/export", nil, h.exportMyData)
	if exported.Code != http.StatusOK {
		t.Fatalf("export = %d: %s", exported.Code, exported.Body.String())
	}
	body := exported.Body.String()
	for _, expected := range []string{"My current-circle task", "My meter", "My thing", `"reading":3`, `"title":"Mine"`} {
		if !strings.Contains(body, expected) {
			t.Fatalf("export omitted authorized data %q: %s", expected, body)
		}
	}
	for _, forbidden := range []string{"Foreign circle task", "Foreign meter", "Foreign thing", `"reading":99`, `"title":"Foreign"`} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("export leaked foreign-circle data %q: %s", forbidden, body)
		}
	}
}

func fullPreferences() map[string]any {
	return map[string]any{
		"theme": "dydan", "appearance": "light", "defaultView": "focus",
		"compactMode": false, "language": "en", "dateFormat": "DD/MM/YYYY",
		"timeFormat": "h:mm A", "firstDayOfWeek": 1, "currency": "GBP",
		"sidebarCollapsed": false, "showItemBadges": true, "rememberSidebar": true,
		"notifications": map[string]any{
			"taskReminders": true, "routineNudges": true, "familyActivity": false,
			"rewards": true, "weeklySummary": false, "quietHours": false,
			"pushChannel": true, "emailChannel": false,
		},
		"privacy": map[string]any{
			"activityVisible": true, "analytics": false, "personalization": false,
		},
		"betaFeatures": false, "verboseLogging": false,
	}
}

func TestImportMyDataRestoresOwnPreferencesOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, _, account := emailChangeHandler(t, &emailChangeSender{})
	valid := func() map[string]any {
		return map[string]any{
			"format":      "dydan-user-export/v1",
			"account":     map[string]any{"id": account.ID, "email": account.Email},
			"preferences": fullPreferences(),
		}
	}

	if resp := callEmailChangeHandler(t, h, nil, "/import", valid(), h.importMyData); resp.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized import = %d", resp.Code)
	}

	badFormat := valid()
	badFormat["format"] = "dydan-user-export/v0"
	if resp := callEmailChangeHandler(t, h, account, "/import", badFormat, h.importMyData); resp.Code != http.StatusBadRequest {
		t.Fatalf("bad format = %d", resp.Code)
	}

	foreignAccount := valid()
	foreignAccount["account"] = map[string]any{"id": account.ID + 1, "email": account.Email}
	if resp := callEmailChangeHandler(t, h, account, "/import", foreignAccount, h.importMyData); resp.Code != http.StatusForbidden {
		t.Fatalf("foreign id = %d (wanted 403)", resp.Code)
	}

	foreignEmail := valid()
	foreignEmail["account"] = map[string]any{"id": account.ID, "email": "other@example.test"}
	if resp := callEmailChangeHandler(t, h, account, "/import", foreignEmail, h.importMyData); resp.Code != http.StatusForbidden {
		t.Fatalf("foreign email = %d (wanted 403)", resp.Code)
	}

	emptyPrefs := valid()
	emptyPrefs["preferences"] = nil
	if resp := callEmailChangeHandler(t, h, account, "/import", emptyPrefs, h.importMyData); resp.Code != http.StatusBadRequest {
		t.Fatalf("missing preferences = %d", resp.Code)
	}

	invalidPrefs := valid()
	invalidPrefs["preferences"] = map[string]any{"theme": "neon"}
	if resp := callEmailChangeHandler(t, h, account, "/import", invalidPrefs, h.importMyData); resp.Code != http.StatusBadRequest {
		t.Fatalf("invalid preferences = %d", resp.Code)
	}

	restored := callEmailChangeHandler(t, h, account, "/import", valid(), h.importMyData)
	if restored.Code != http.StatusOK {
		t.Fatalf("valid import = %d: %s", restored.Code, restored.Body.String())
	}
	stored, err := h.loadPreferences(context.Background(), account.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.Theme != "dydan" || stored.Currency != "GBP" || stored.FirstDayOfWeek != 1 || stored.DateFormat != "DD/MM/YYYY" {
		t.Fatalf("restored preferences = %+v", stored)
	}
	if stored.VerboseLogging || stored.BetaFeatures {
		t.Fatalf("restore flipped experimental flags unexpectedly: %+v", stored)
	}
}

func TestIntegrationStatusReportsRealCapabilities(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open("file:integrations?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&uModel.User{}, &cModel.Circle{}, &cModel.UserCircle{}); err != nil {
		t.Fatal(err)
	}
	expiration := time.Now().Add(24 * time.Hour)
	webhook := "https://example.test/hooks/dydan"
	account := &uModel.User{ID: 7, Username: "admin", Email: "admin@example.test", CircleID: 3, Expiration: &expiration}
	if err := db.Create(account).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.Circle{ID: 3, Name: "Team"}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&cModel.UserCircle{UserID: 7, CircleID: 3, Role: cModel.UserRoleAdmin, IsActive: true}).Error; err != nil {
		t.Fatal(err)
	}
	h := &Handler{
		userRepo:           uRepo.NewUserRepository(db, &config.Config{}),
		circleRepo:         cRepo.NewCircleRepository(db),
		serveSwagger:       false,
		serverVersion:      "test-build",
		serverCommit:       "abc123",
		serverBuildDate:    "2026-09-26T00:00:00Z",
		deployment:         "docker",
		serverOS:           "linux",
		serverArchitecture: "amd64",
		databaseType:       "sqlite",
		storageType:        "local",
	}
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/integrations", nil)
	auth.SetCurrentUser(ctx, &uModel.UserDetails{User: *account, WebhookURL: &webhook})
	h.getIntegrationStatus(ctx)
	resp := recorder
	if resp.Code != http.StatusOK {
		t.Fatalf("integrations = %d: %s", resp.Code, resp.Body.String())
	}
	var body struct {
		Res struct {
			Server struct {
				Version         string `json:"version"`
				Commit          string `json:"commit"`
				BuildDate       string `json:"buildDate"`
				Deployment      string `json:"deployment"`
				OperatingSystem string `json:"operatingSystem"`
				Architecture    string `json:"architecture"`
				Database        string `json:"database"`
				Storage         string `json:"storage"`
			} `json:"server"`
			APIDocs struct {
				Enabled bool   `json:"enabled"`
				URL     string `json:"url"`
			} `json:"apiDocs"`
			GoogleCalendar struct {
				Available bool `json:"available"`
			} `json:"googleCalendar"`
			Webhooks struct {
				Available bool   `json:"available"`
				URL       string `json:"url"`
				Editable  bool   `json:"editable"`
				PlusOnly  bool   `json:"plusOnly"`
			} `json:"webhooks"`
		} `json:"res"`
	}
	if err := json.Unmarshal(resp.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Res.APIDocs.Enabled {
		t.Fatal("serve_swagger=false must report apiDocs.disabled")
	}
	if body.Res.Server.Version != "test-build" || body.Res.Server.Commit != "abc123" || body.Res.Server.BuildDate != "2026-09-26T00:00:00Z" || body.Res.Server.Deployment != "docker" || body.Res.Server.OperatingSystem != "linux" || body.Res.Server.Architecture != "amd64" || body.Res.Server.Database != "sqlite" || body.Res.Server.Storage != "local" {
		t.Fatalf("server info = %+v", body.Res.Server)
	}
	if body.Res.GoogleCalendar.Available {
		t.Fatal("no google calendar credential configured")
	}
	if !body.Res.Webhooks.Available || body.Res.Webhooks.URL != webhook || !body.Res.Webhooks.Editable || body.Res.Webhooks.PlusOnly {
		t.Fatalf("webhook status = %+v", body.Res.Webhooks)
	}
}

func TestVerboseRequestLoggingHonorsPreference(t *testing.T) {
	gin.SetMode(gin.TestMode)
	h, db, account := emailChangeHandler(t, &emailChangeSender{})

	run := func() string {
		var buf bytes.Buffer
		core := zapcore.NewCore(
			zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()),
			zapcore.AddSync(&buf),
			zapcore.DebugLevel,
		)
		logger := zap.New(core).Sugar()
		recorder := httptest.NewRecorder()
		ctx, _ := gin.CreateTestContext(recorder)
		ctx.Request = httptest.NewRequest(http.MethodGet, "/users/profile", nil)
		ctx.Request = ctx.Request.WithContext(logging.WithLogger(ctx, logger))
		auth.SetCurrentUser(ctx, &uModel.UserDetails{User: *account})
		h.verboseRequestLogger()(ctx)
		return buf.String()
	}

	// Default preferences leave VerboseLogging=false: no per-request log.
	if logged := run(); strings.Contains(logged, "verbose.request") {
		t.Fatalf("verbose log emitted while disabled: %s", logged)
	}

	prefs := fullPreferences()
	prefs["verboseLogging"] = true
	encoded, err := json.Marshal(prefs)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&uModel.UserPreference{UserID: account.ID, Value: string(encoded)}).Error; err != nil {
		t.Fatal(err)
	}

	logged := run()
	if !strings.Contains(logged, "verbose.request") || !strings.Contains(logged, `"userID":1`) {
		t.Fatalf("verbose log missing when enabled: %s", logged)
	}
}
