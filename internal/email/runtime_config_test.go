package email

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/alamoudimoh/dydan-server/config"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newRuntimeStoreForTest(t *testing.T) *RuntimeStore {
	t.Helper()
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&RuntimeConfiguration{}); err != nil {
		t.Fatal(err)
	}
	return NewRuntimeStore(&config.Config{EmailConfig: config.EmailConfig{
		RuntimeSecret: "a test-only runtime-email encryption secret that is long enough",
	}}, db)
}

func smtpInput(secret string) DeliveryInput {
	return DeliveryInput{
		Provider:  "smtp",
		FromEmail: "mailer@example.com",
		Host:      "smtp.example.com",
		Port:      587,
		Username:  "mailer",
		Secret:    secret,
		AppHost:   "https://app.example.com",
	}
}

func TestRuntimeStoreEncryptsAndMasksDeliverySecret(t *testing.T) {
	store := newRuntimeStoreForTest(t)
	if _, err := store.Save(context.Background(), smtpInput("smtp-password")); err != nil {
		t.Fatal(err)
	}

	status, err := store.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Configured || status.Source != "runtime" || !status.SecretConfigured {
		t.Fatalf("unexpected masked status: %#v", status)
	}
	encoded, err := json.Marshal(status)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "smtp-password") {
		t.Fatalf("status leaked secret: %s", encoded)
	}

	var record RuntimeConfiguration
	if err := store.db.First(&record, 1).Error; err != nil {
		t.Fatal(err)
	}
	if record.SecretCiphertext == "smtp-password" || strings.Contains(record.SecretCiphertext, "smtp-password") {
		t.Fatalf("secret was not encrypted at rest: %#v", record)
	}

	current, err := store.currentConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if current.EmailConfig.Key != "smtp-password" {
		t.Fatalf("decrypted key = %q", current.EmailConfig.Key)
	}
}

func TestRuntimeStoreRetainsSavedSecretWhenOmitted(t *testing.T) {
	store := newRuntimeStoreForTest(t)
	if _, err := store.Save(context.Background(), smtpInput("first-secret")); err != nil {
		t.Fatal(err)
	}
	update := smtpInput("")
	update.Host = "relay.example.com"
	if _, err := store.Save(context.Background(), update); err != nil {
		t.Fatal(err)
	}
	current, err := store.currentConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if current.EmailConfig.Key != "first-secret" || current.EmailConfig.Host != "relay.example.com" {
		t.Fatalf("saved config did not preserve secret/update fields: %#v", current.EmailConfig)
	}
}

func TestRuntimeStoreUsesEnvironmentFallbackUntilSaved(t *testing.T) {
	store := newRuntimeStoreForTest(t)
	store.cfg.EmailConfig = config.EmailConfig{
		Provider: "smtp", Email: "env@example.com", Host: "smtp.env.example.com", Port: 2525,
		User: "env-user", Key: "env-secret", AppHost: "https://env.example.com",
		RuntimeSecret: store.cfg.EmailConfig.RuntimeSecret,
	}
	status, err := store.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !status.Configured || status.Source != "environment" || status.FromEmail != "env@example.com" {
		t.Fatalf("unexpected fallback status: %#v", status)
	}
}

type captureRoundTripper struct {
	body    []byte
	headers http.Header
}

func (r *captureRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	r.headers = req.Header.Clone()
	r.body, _ = io.ReadAll(req.Body)
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"data":{"succeeded":1}}`)),
		Request:    req,
	}, nil
}

func TestSMTP2GoSendTestEmailUsesConfiguredTransport(t *testing.T) {
	transport := &captureRoundTripper{}
	sender := &SMTP2GoSender{
		apiKey:           "smtp2go-key",
		fromMail:         "mailer@example.com",
		appHost:          "https://app.example.com",
		verifyTemplateID: "verify-template",
		httpClient:       &http.Client{Transport: transport},
	}
	if err := sender.SendTestEmail(context.Background(), "admin@example.com"); err != nil {
		t.Fatal(err)
	}
	if transport.headers.Get("X-Smtp2go-Api-Key") != "smtp2go-key" {
		t.Fatal("SMTP2GO API key was not sent through configured transport")
	}
	var request smtp2goTemplateRequest
	if err := json.Unmarshal(transport.body, &request); err != nil {
		t.Fatal(err)
	}
	if request.TemplateID != "verify-template" || len(request.To) != 1 || request.To[0] != "admin@example.com" {
		t.Fatalf("unexpected SMTP2GO test request: %#v", request)
	}
}
