package payment

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stripe/stripe-go/v76/webhook"

	"github.com/alamoudimoh/daydan-server/config"
	pModel "github.com/alamoudimoh/daydan-server/external/payment/model"
	prepo "github.com/alamoudimoh/daydan-server/external/payment/repo"
	authn "github.com/alamoudimoh/daydan-server/internal/auth"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

const testWebhookSecret = "whsec_test_secret_0123456789abcdef"

type fixture struct {
	router *gin.Engine
	db     *gorm.DB
	h      *Webhook
}

// seedCustomer links cus_123 to a user so checkout.session.completed
// persists the unified subscription row.
func seedCustomer(t *testing.T, db *gorm.DB) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.Create(&pModel.StripeCustomer{
		CustomerID: "cus_123",
		UserID:     1,
		CircleID:   2,
		CreatedAt:  &now,
	}).Error; err != nil {
		t.Fatalf("seed customer: %v", err)
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "stripe-test.db")), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(
		&pModel.StripeCustomer{},
		&pModel.StripeSubscription{},
		&pModel.StripeSession{},
		&pModel.StripeInvoice{},
		&pModel.Subscription{},
		&pModel.RevenueCatEvent{},
		&pModel.StripeEvent{},
	); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	cfg := config.NewConfig()
	cfg.StripeConfig.WebhookSecret = testWebhookSecret
	cfg.StripeConfig.WhitelistedIPs = nil
	f := &fixture{
		db: db,
		h: NewWebhook(
			prepo.NewStripeDB(db),
			prepo.NewRevenueCatDB(db),
			prepo.NewSubscriptionDB(db),
			nil,
			nil,
			cfg,
		),
	}
	seedCustomer(t, db)
	f.router = gin.New()
	f.router.Use(authn.LimitRequestBody(authn.MaxAuthRequestBodyBytes))
	f.router.POST("/webhooks/stripe", f.h.StripeWebhook)
	return f
}

// signedEvent builds a Stripe checkout.session.completed event payload and
// its Stripe-Signature header, exactly as Stripe would deliver it.
func signedEvent(t *testing.T, secret string, eventID string, objectJSON []byte, ts time.Time) ([]byte, string) {
	t.Helper()
	payload := fmt.Sprintf(`{"id":%q,"object":"event","api_version":"2023-10-16","created":%d,"data":{"object":%s},"livemode":false,"pending_webhooks":1,"request":{"id":null,"idempotency_key":null},"type":"checkout.session.completed"}`,
		eventID, ts.Unix(), string(objectJSON))
	sp := webhook.GenerateTestSignedPayload(&webhook.UnsignedPayload{
		Payload:   []byte(payload),
		Secret:    secret,
		Timestamp: ts,
	})
	return sp.Payload, sp.Header
}

func paidSessionObject() string {
	return `{"id":"cs_test_123","object":"checkout.session","payment_status":"paid","status":"complete","customer":{"id":"cus_123"},"subscription":{"id":"sub_123"}}`
}

func post(f *fixture, body []byte, sigHeader string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/webhooks/stripe", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Stripe-Signature", sigHeader)
	w := httptest.NewRecorder()
	f.router.ServeHTTP(w, req)
	return w
}

func subscriptionCount(t *testing.T, f *fixture) int64 {
	t.Helper()
	var count int64
	if err := f.db.Model(&pModel.Subscription{}).Count(&count).Error; err != nil {
		t.Fatalf("count subscriptions: %v", err)
	}
	return count
}

func TestStripeValidSignedEventIsProcessed(t *testing.T) {
	f := newFixture(t)
	body, sig := signedEvent(t, testWebhookSecret, "evt_ok", []byte(paidSessionObject()), time.Now())
	w := post(f, body, sig)
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, body=%s", w.Code, w.Body.String())
	}
	if got := subscriptionCount(t, f); got != 1 {
		t.Fatalf("subscriptions = %d, want 1", got)
	}
	var ev pModel.StripeEvent
	if err := f.db.Where("event_id = ?", "evt_ok").First(&ev).Error; err != nil {
		t.Fatalf("claim row missing after success: %v", err)
	}
	if ev.Status != "processed" {
		t.Fatalf("status = %q, want processed", ev.Status)
	}
}

func TestStripeMissingSignatureRejected(t *testing.T) {
	f := newFixture(t)
	body, _ := signedEvent(t, testWebhookSecret, "evt_nosig", []byte(paidSessionObject()), time.Now())
	w := post(f, body, "")
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := subscriptionCount(t, f); got != 0 {
		t.Fatal("side effects executed without signature")
	}
}

func TestStripeWrongSecretRejected(t *testing.T) {
	f := newFixture(t)
	body, sig := signedEvent(t, "whsec_attacker_controlled_secret_000", "evt_wrongsecret", []byte(paidSessionObject()), time.Now())
	w := post(f, body, sig)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := subscriptionCount(t, f); got != 0 {
		t.Fatal("side effects executed with wrong-secret signature")
	}
}

func TestStripeTamperedBodyRejected(t *testing.T) {
	f := newFixture(t)
	body, sig := signedEvent(t, testWebhookSecret, "evt_tamper", []byte(paidSessionObject()), time.Now())
	// Flip one byte of the (attacker-desirable) subscription id AFTER signing.
	tampered := bytes.Replace(body, []byte("sub_123"), []byte("sub_999"), 1)
	if bytes.Equal(tampered, body) {
		t.Fatal("tamper substitution did not change the body")
	}
	w := post(f, tampered, sig)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := subscriptionCount(t, f); got != 0 {
		t.Fatal("tampered body produced side effects")
	}
}

func TestStripeStaleTimestampRejected(t *testing.T) {
	f := newFixture(t)
	old := time.Now().Add(-15 * time.Minute)
	body, sig := signedEvent(t, testWebhookSecret, "evt_stale", []byte(paidSessionObject()), old)
	w := post(f, body, sig)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", w.Code)
	}
	if got := subscriptionCount(t, f); got != 0 {
		t.Fatal("stale event produced side effects")
	}
}

func TestStripeMalformedHeaderRejected(t *testing.T) {
	f := newFixture(t)
	body, _ := signedEvent(t, testWebhookSecret, "evt_malformed", []byte(paidSessionObject()), time.Now())
	for _, header := range []string{"not-a-signature", "t=abc,v1=zzz", "v1=", "t=123"} {
		w := post(f, body, header)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("header %q: status = %d, want 400", header, w.Code)
		}
	}
	if got := subscriptionCount(t, f); got != 0 {
		t.Fatal("malformed headers produced side effects")
	}
}

func TestStripeDuplicateSequentialDeliveryProcessedOnce(t *testing.T) {
	f := newFixture(t)
	body, sig := signedEvent(t, testWebhookSecret, "evt_dup_seq", []byte(paidSessionObject()), time.Now())
	if w := post(f, body, sig); w.Code != http.StatusOK {
		t.Fatalf("first delivery status = %d", w.Code)
	}
	w := post(f, body, sig)
	if w.Code != http.StatusOK {
		t.Fatalf("duplicate delivery must ACK 200 for Stripe to stop retrying, got %d", w.Code)
	}
	if got := subscriptionCount(t, f); got != 1 {
		t.Fatalf("subscriptions after duplicate delivery = %d, want 1", got)
	}
}

func TestStripeConcurrentDuplicateDeliveryProcessedOnce(t *testing.T) {
	f := newFixture(t)
	body, sig := signedEvent(t, testWebhookSecret, "evt_dup_conc", []byte(paidSessionObject()), time.Now())

	var wg sync.WaitGroup
	codes := make([]int, 8)
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			codes[i] = post(f, body, sig).Code
		}(i)
	}
	wg.Wait()
	for i, code := range codes {
		if code != http.StatusOK {
			t.Fatalf("delivery %d status = %d, want 200", i, code)
		}
	}
	if got := subscriptionCount(t, f); got != 1 {
		t.Fatalf("subscriptions after concurrent duplicates = %d, want 1", got)
	}
}

func TestStripeDuplicateAfterRestartStillSuppressed(t *testing.T) {
	f := newFixture(t)
	body, sig := signedEvent(t, testWebhookSecret, "evt_restart", []byte(paidSessionObject()), time.Now())
	if w := post(f, body, sig); w.Code != http.StatusOK {
		t.Fatalf("first delivery status = %d", w.Code)
	}
	// Simulate a process restart: a brand-new handler/webhook stack over the
	// SAME durable database (the claim rows survive restarts).
	f2 := newFixtureOnDB(t, f.db)
	w := post(f2, body, sig)
	if w.Code != http.StatusOK {
		t.Fatalf("post-restart duplicate status = %d", w.Code)
	}
	if got := subscriptionCount(t, f); got != 1 {
		t.Fatalf("subscriptions after restart replay = %d, want 1", got)
	}
}

func TestStripeRetryAfterFailedProcessingIsReprocessed(t *testing.T) {
	f := newFixture(t)
	// The inner data.object fails to unmarshal into CheckoutSession, so the
	// verified event errors mid-processing and returns 500. The claim row
	// MUST be released so the legitimate retry below is processed.
	badObject := `{"id":"cs_bad","object":"checkout.session","payment_status":"paid","status":"complete","created":"not-an-int"}`
	body, sig := signedEvent(t, testWebhookSecret, "evt_retry", []byte(badObject), time.Now())
	if w := post(f, body, sig); w.Code != http.StatusInternalServerError {
		t.Fatalf("failing processing status = %d, want 500", w.Code)
	}
	var count int64
	f.db.Model(&pModel.StripeEvent{}).Count(&count)
	if count != 0 {
		t.Fatal("failed processing left its claim behind; retry would be swallowed")
	}
	// Stripe legitimately retries the same event with corrected content.
	goodBody, goodSig := signedEvent(t, testWebhookSecret, "evt_retry", []byte(paidSessionObject()), time.Now())
	w := post(f, goodBody, goodSig)
	if w.Code != http.StatusOK {
		t.Fatalf("retry status = %d, body=%s", w.Code, w.Body.String())
	}
	if got := subscriptionCount(t, f); got != 1 {
		t.Fatalf("subscriptions after retry = %d, want 1", got)
	}
}

func TestStripeOversizedBodyRejected(t *testing.T) {
	f := newFixture(t)
	big := make([]byte, authn.MaxAuthRequestBodyBytes+1)
	for i := range big {
		big[i] = 'a'
	}
	w := post(f, big, "")
	if w.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized body status = %d, want 413", w.Code)
	}
}

func TestStripeUnconfiguredSecretFailsClosed(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "no-secret.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&pModel.StripeEvent{}, &pModel.Subscription{}); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewConfig()
	cfg.StripeConfig.WebhookSecret = "" // integration misconfigured
	f := &fixture{
		db: db,
		h: NewWebhook(prepo.NewStripeDB(db), prepo.NewRevenueCatDB(db), prepo.NewSubscriptionDB(db), nil, nil, cfg),
	}
	f.router = gin.New()
	f.router.POST("/webhooks/stripe", f.h.StripeWebhook)

	body, sig := signedEvent(t, testWebhookSecret, "evt_unconf", []byte(paidSessionObject()), time.Now())
	w := post(f, body, sig)
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("unconfigured secret status = %d, want 503 (fail closed)", w.Code)
	}
	if got := subscriptionCount(t, f); got != 0 {
		t.Fatal("unsigned-accepting fallback executed side effects")
	}
}

func TestStripeIPAllowlistRemainsDefenseInDepth(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "ip.db")), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&pModel.StripeEvent{}, &pModel.Subscription{}); err != nil {
		t.Fatal(err)
	}
	cfg := config.NewConfig()
	cfg.StripeConfig.WebhookSecret = testWebhookSecret
	cfg.StripeConfig.WhitelistedIPs = []string{"203.0.113.7"}
	h := NewWebhook(prepo.NewStripeDB(db), prepo.NewRevenueCatDB(db), prepo.NewSubscriptionDB(db), nil, nil, cfg)
	router := gin.New()
	router.POST("/webhooks/stripe", h.StripeWebhook)
	f := &fixture{db: db, h: h, router: router}

	body, sig := signedEvent(t, testWebhookSecret, "evt_ip", []byte(paidSessionObject()), time.Now())
	// httptest requests originate from 127.0.0.1 — not on the allowlist.
	w := post(f, body, sig)
	if w.Code != http.StatusForbidden {
		t.Fatalf("non-allowlisted IP status = %d, want 403", w.Code)
	}
	if got := subscriptionCount(t, f); got != 0 {
		t.Fatal("IP-rejected delivery produced side effects")
	}
}

// Regression: the pre-Phase-8 handler dumped every raw event payload to a
// w-<timestamp>-<type>.json file in the working directory. Webhook payloads
// contain customer PII and must never hit disk.
func TestStripeNoRawPayloadDumpedToDisk(t *testing.T) {
	dir := t.TempDir()
	oldWd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(oldWd) })

	f := newFixture(t)
	body, sig := signedEvent(t, testWebhookSecret, "evt_nodisk", []byte(paidSessionObject()), time.Now())
	if w := post(f, body, sig); w.Code != http.StatusOK {
		t.Fatalf("status = %d", w.Code)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), "w-") && strings.HasSuffix(e.Name(), ".json") {
			t.Fatalf("raw Stripe payload written to disk: %s", e.Name())
		}
	}
}

func newFixtureOnDB(t *testing.T, db *gorm.DB) *fixture {	t.Helper()
	gin.SetMode(gin.TestMode)
	cfg := config.NewConfig()
	cfg.StripeConfig.WebhookSecret = testWebhookSecret
	f := &fixture{
		db: db,
		h: NewWebhook(prepo.NewStripeDB(db), prepo.NewRevenueCatDB(db), prepo.NewSubscriptionDB(db), nil, nil, cfg),
	}
	f.router = gin.New()
	f.router.POST("/webhooks/stripe", f.h.StripeWebhook)
	return f
}

// Adversarial: any processing failure path — including a panic recovered by
// the handler guard — must release the durable claim so Stripe's retry is
// processed instead of suppressed forever.
func TestStripeClaimReleasedWhenBackingTableMissing(t *testing.T) {
	f := newFixture(t)
	if err := f.db.Migrator().DropTable("stripe_sessions"); err != nil {
		t.Fatal(err)
	}
	body, sig := signedEvent(t, testWebhookSecret, "evt_panic", []byte(paidSessionObject()), time.Now())
	w := post(f, body, sig)
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", w.Code)
	}
	var count int64
	f.db.Model(&pModel.StripeEvent{}).Count(&count)
	if count != 0 {
		t.Fatal("claim row survived failed processing; retries would be swallowed")
	}
}
