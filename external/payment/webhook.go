package payment

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alamoudimoh/daydan-server/internal/utils"
	"github.com/alamoudimoh/daydan-server/logging"

	"github.com/alamoudimoh/daydan-server/config"
	"github.com/alamoudimoh/daydan-server/external/payment/model"
	pDB "github.com/alamoudimoh/daydan-server/external/payment/repo"
	stripeService "github.com/alamoudimoh/daydan-server/external/payment/service"
	authn "github.com/alamoudimoh/daydan-server/internal/auth"
	uRepo "github.com/alamoudimoh/daydan-server/internal/user/repo"
	jwt "github.com/appleboy/gin-jwt/v2"
	"github.com/gin-gonic/gin"
	stripe "github.com/stripe/stripe-go/v76"
	"github.com/stripe/stripe-go/v76/webhook"
	"go.uber.org/zap"
)

// DefaultWebhookTolerance matches Stripe's recommended signature freshness
// window. Configurable via stripe.webhook_tolerance (hard-capped at 24h in
// config validation).
const DefaultWebhookTolerance = webhook.DefaultTolerance

type Webhook struct {
	stripeDB         pDB.StripeDB
	revenueCatDB     pDB.RevenueCatDB
	subscriptionDB   pDB.SubscriptionDB
	whitelistIPs     map[string]bool
	stripe           *stripeService.StripeService
	prices           []config.StripePrices
	revenueCatConfig config.RevenueCatConfig
	userRepo         *uRepo.UserRepository

	webhookSecret string
	tolerance     time.Duration
}

func NewWebhook(stripeDB pDB.StripeDB,
	revenueCatDB pDB.RevenueCatDB,
	subscriptionDB pDB.SubscriptionDB,
	stripeService *stripeService.StripeService,
	uRepo *uRepo.UserRepository,
	config *config.Config) *Webhook {

	whitelistIPs := make(map[string]bool)
	for _, ip := range config.StripeConfig.WhitelistedIPs {
		whitelistIPs[ip] = true
	}

	tolerance := config.StripeConfig.WebhookTolerance
	if tolerance <= 0 {
		tolerance = DefaultWebhookTolerance
	}

	return &Webhook{
		stripeDB:         stripeDB,
		revenueCatDB:     revenueCatDB,
		subscriptionDB:   subscriptionDB,
		whitelistIPs:     whitelistIPs,
		stripe:           stripeService,
		userRepo:         uRepo,
		prices:           config.StripeConfig.Prices,
		revenueCatConfig: config.RevenueCatConfig,
		webhookSecret:    config.StripeConfig.WebhookSecret,
		tolerance:        tolerance,
	}
}

// webhook handler
func (h *Webhook) isIPWhitelisted(ip string) bool {
	_, ok := h.whitelistIPs[ip]
	return ok
}

// StripeWebhook handles inbound Stripe events.
//
// Trust model (Phase 8): authenticity comes exclusively from the Stripe
// signature over the raw request body, verified with the configured endpoint
// secret (stripe.webhook_secret). The IP allowlist — if configured — is kept
// as defense-in-depth only; it is never sufficient on its own. Without a
// configured secret the endpoint fails closed with 503 rather than accepting
// unsigned events.
//
// Replay model: every verified event claims its stable Stripe event ID in
// the stripe_events table before business logic runs; duplicates get a 200
// without re-executing side effects, and a failed processing run releases
// the claim so Stripe's retry is processed again.
func (h *Webhook) StripeWebhook(c *gin.Context) {

	logger := logging.FromContext(c)

	if h.webhookSecret == "" {
		// Fail closed: an operator enabled Stripe but did not configure the
		// endpoint secret. Never silently fall back to network trust.
		logger.Error("stripe.webhook rejected: no stripe.webhook_secret configured")
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "stripe webhook verification is not configured"})
		return
	}

	// Defense-in-depth only: enforce legacy IP restrictions when present.
	ip := c.ClientIP()
	if len(h.whitelistIPs) > 0 && !h.isIPWhitelisted(ip) {
		logger.Errorw("payment.webhook.Webhook failed to validate ip", "ip", ip)
		c.JSON(http.StatusForbidden, "Forbidden")
		return
	}

	// The signature covers the exact bytes Stripe sent. Read the raw body
	// once and verify against it — never re-marshal or re-indent first.
	rawBody, err := c.GetRawData()
	if err != nil {
		if authn.IsBodyTooLarge(err) {
			c.JSON(http.StatusRequestEntityTooLarge, gin.H{"error": "request body too large"})
			return
		}
		logger.Errorw("payment.webhook.Webhook failed to read body", "err", err)
		c.JSON(http.StatusBadRequest, "Invalid request body")
		return
	}
	if len(rawBody) == 0 {
		logger.Errorw("payment.webhook.Webhook failed to read body", "err", "empty body")
		c.JSON(http.StatusBadRequest, "Invalid request body")
		return
	}

	sigHeader := c.GetHeader("Stripe-Signature")
	event, err := webhook.ConstructEventWithTolerance(rawBody, sigHeader, h.webhookSecret, h.tolerance)
	if err != nil {
		// Deliberately terse: error strings can embed attacker-controlled
		// header fragments; log the failure category only.
		logger.Errorw("stripe.webhook signature verification failed", "reason", signatureErrorKind(err))
		c.JSON(http.StatusBadRequest, gin.H{"error": "signature verification failed"})
		return
	}

	claimed, err := h.stripeDB.ClaimStripeEvent(c, &model.StripeEvent{
		EventID:     event.ID,
		EventType:   string(event.Type),
		Livemode:    event.Livemode,
		Status:      "processing",
		ProcessedAt: time.Now().UTC(),
	})
	if err != nil {
		logger.Errorw("stripe.webhook failed to claim event", "event_id", event.ID, "err", err)
		c.JSON(http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if !claimed {
		logger.Debugw("stripe.webhook duplicate delivery ignored", "event_id", event.ID)
		c.JSON(http.StatusOK, gin.H{"status": "duplicate"})
		return
	}

	// A panic during business processing must NOT leave the claim row
	// behind: gin's recovery middleware would otherwise answer 500 while the
	// durable claim suppresses every future legitimate retry of this event.
	processErr := func() (err error) {
		defer func() {
			if r := recover(); r != nil {
				logger.Errorw("stripe.webhook processing panicked", "event_id", event.ID)
				err = fmt.Errorf("processing panicked")
			}
		}()
		return h.processStripeEvent(c, logger, &event)
	}()
	if processErr != nil {
		// Release the claim so Stripe's legitimate retry of this event is
		// processed again instead of being permanently suppressed.
		if relErr := h.stripeDB.ReleaseStripeEvent(c, event.ID); relErr != nil {
			logger.Errorw("stripe.webhook failed to release event claim", "event_id", event.ID, "err", relErr)
		}
		logger.Errorw("stripe.webhook failed to process event", "event_id", event.ID, "err", err)
		c.JSON(http.StatusInternalServerError, "Internal Server Error")
		return
	}

	if err := h.stripeDB.MarkStripeEventProcessed(c, event.ID); err != nil {
		// Non-fatal: idempotency is already guaranteed by the claim row;
		// status bookkeeping is best-effort observability.
		logger.Errorw("stripe.webhook failed to mark event processed", "event_id", event.ID, "err", err)
	}
	logger.Debugw("payment.webhook.Webhook handled event", "event_id", event.ID, "event", event.Type)
}

// signatureErrorKind classifies signature failures for logging without
// echoing untrusted header content.
func signatureErrorKind(err error) string {
	msg := err.Error()
	switch {
	case errors.Is(err, webhook.ErrInvalidHeader):
		return "malformed_signature_header"
	case strings.Contains(msg, "No signatures found"):
		return "missing_signature"
	case strings.Contains(msg, "timestamp"):
		return "timestamp_out_of_tolerance"
	case strings.Contains(msg, "signature mismatch"), strings.Contains(msg, "Signature hash"):
		return "invalid_signature"
	default:
		return "unverified_payload"
	}
}

// processStripeEvent executes business side effects for a verified,
// claim-held event.
func (h *Webhook) processStripeEvent(c *gin.Context, logger *zap.SugaredLogger, event *stripe.Event) error {

	switch event.Type {
	case "checkout.session.completed":
		var s stripe.CheckoutSession
		if err := json.Unmarshal(event.Data.Raw, &s); err != nil {
			return fmt.Errorf("unmarshal checkout session: %w", err)
		}

		if s.PaymentStatus != "paid" || s.Status != "complete" {
			logger.Errorw("payment.webhook.Webhook failed to get payment status", "event", event.Type)
			return nil
		}

		if s.Subscription.ID == "" {
			logger.Errorw("payment.webhook.Webhook failed to get subscription id", "event", event.Type)
			return nil
		}

		// Get user ID from stripe customer
		customer, err := h.stripeDB.GetCustomerByCustomerID(c, s.Customer.ID)
		if err != nil {
			return fmt.Errorf("get customer %s: %w", s.Customer.ID, err)
		}

		expiredAt := time.Now().UTC().AddDate(1, 0, 0)
		// Save to legacy table for backward compatibility
		if _, err := h.stripeDB.SaveSubscription(c, &model.StripeSubscription{
			SubscriptionID: s.Subscription.ID,
			CustomerID:     s.Customer.ID,
			Status:         "active",
			ExpiredAt:      &expiredAt,
		}); err != nil {
			return fmt.Errorf("save legacy subscription: %w", err)
		}

		// Save to unified subscription table
		if customer != nil {
			unifiedSub := &model.Subscription{
				ID:                     s.Subscription.ID,
				UserID:                 int(customer.UserID),
				CircleID:               customer.CircleID,
				Provider:               model.SubscriptionProviderStripe,
				ExternalSubscriptionID: s.Subscription.ID,
				ExternalCustomerID:     s.Customer.ID,
				Status:                 "active",
				ExpiresAt:              &expiredAt,
				CreatedAt:              time.Now().UTC(),
				UpdatedAt:              time.Now().UTC(),
			}
			if _, err := h.subscriptionDB.SaveSubscription(c, unifiedSub); err != nil {
				return fmt.Errorf("save unified subscription: %w", err)
			}
		}

		if err := h.stripeDB.UpdateSession(c, event.ID, string(s.Status)); err != nil {
			return fmt.Errorf("update session: %w", err)
		}

	case "invoice.payment_succeeded":
		var inv stripe.Invoice
		if err := json.Unmarshal(event.Data.Raw, &inv); err != nil {
			return fmt.Errorf("unmarshal invoice: %w", err)
		}
		mInvoice := &model.StripeInvoice{
			InvoiceID:      inv.ID,
			CustomerID:     inv.Customer.ID,
			Status:         "paid",
			PeriodStart:    time.Unix(int64(inv.PeriodStart), 0),
			PeriodEnd:      time.Unix(int64(inv.PeriodEnd), 0),
			SubscriptionID: inv.Subscription.ID,
			ReceivedAt:     time.Now().UTC(),
		}
		expiredAt := time.Unix(int64(inv.PeriodEnd), 0)

		if inv.PeriodEnd == inv.PeriodStart {
			// on initial payment, the period end and start are the same
			logger.Debugw("payment.webhook ignoring payment_succeeded for session checkout", "event", event.Type)
		}
		expiredAt = mInvoice.PeriodEnd.AddDate(1, 0, 0)

		if _, err := h.stripeDB.SaveInvoice(c, mInvoice); err != nil {
			return fmt.Errorf("save invoice: %w", err)
		}
		now := time.Now().UTC()

		// Update legacy table
		if err := h.stripeDB.UpdateSubscription(c, &model.StripeSubscription{
			SubscriptionID: inv.Subscription.ID,
			UpdatedAt:      &now,
			Status:         "active",
			ExpiredAt:      &expiredAt,
		}); err != nil {
			return fmt.Errorf("update legacy subscription: %w", err)
		}

		// Update unified subscription table
		if unifiedSub, err := h.subscriptionDB.GetSubscriptionByID(c, inv.Subscription.ID); err == nil && unifiedSub != nil {
			unifiedSub.Status = "active"
			unifiedSub.ExpiresAt = &expiredAt
			if err := h.subscriptionDB.UpdateSubscription(c, unifiedSub); err != nil {
				return fmt.Errorf("update unified subscription: %w", err)
			}
		} else if err != nil {
			return fmt.Errorf("get unified subscription: %w", err)
		}

	default:
		logger.Debugw("payment.webhook ignoring unhandled event", "event", event.Type)
	}
	return nil
}

// RevenueCat webhook event structure
type RevenueCatWebhookEvent struct {
	APIVersion string          `json:"api_version"`
	Event      RevenueCatEvent `json:"event"`
}

type RevenueCatEvent struct {
	ID                string                         `json:"id"`
	Type              string                         `json:"type"`
	EventTimestampMs  int64                          `json:"event_timestamp_ms"`
	AppUserID         string                         `json:"app_user_id"`
	OriginalAppUserID string                         `json:"original_app_user_id"`
	ProductID         string                         `json:"product_id"`
	EntitlementIDs    []string                       `json:"entitlement_ids"`
	Store             string                         `json:"store"`
	PurchasedAtMs     *int64                         `json:"purchased_at_ms"`
	ExpirationAtMs    *int64                         `json:"expiration_at_ms"`
	Price             *float64                       `json:"price"`
	Currency          string                         `json:"currency"`
	Entitlements      map[string]interface{}         `json:"entitlements"`
	Subscriber        RevenueCatSubscriberAttributes `json:"subscriber_attributes"`
	Environment       string                         `json:"environment"`
	AppID             string                         `json:"app_id"`
	Aliases           []string                       `json:"aliases"`
	Transactions      []RevenueCatTransaction        `json:"transactions"`
}

type RevenueCatSubscriberAttributes struct {
	UserID struct {
		Value     string `json:"value"`
		UpdatedAt int64  `json:"updated_at_ms"`
	} `json:"$userId"`
}

type RevenueCatTransaction struct {
	ID                    string `json:"id"`
	OriginalTransactionID string `json:"original_transaction_id"`
	ProductID             string `json:"product_id"`
	PurchaseDateMs        int64  `json:"purchase_date_ms"`
	ExpiresDateMs         *int64 `json:"expires_date_ms"`
	IsTrialPeriod         bool   `json:"is_trial_period"`
	AutoRenewStatus       *bool  `json:"auto_renew_status"`
	PeriodType            string `json:"period_type"`
	Store                 string `json:"store"`
	Environment           string `json:"environment"`
}

func (h *Webhook) RevenueCatWebhook(c *gin.Context) {
	logger := logging.FromContext(c)

	if strings.TrimSpace(h.revenueCatConfig.AuthSecret) == "" {
		logger.Error("revenuecat.webhook.RevenueCatWebhook is not configured")
		c.JSON(http.StatusServiceUnavailable, "RevenueCat webhook is not configured")
		return
	}

	authHeader := c.GetHeader("Authorization")
	if subtle.ConstantTimeCompare([]byte(authHeader), []byte(h.revenueCatConfig.AuthSecret)) != 1 {
		// Log only the failure category. The Authorization header carries the
		// webhook secret and must never be written to logs.
		logger.Errorw("revenuecat.webhook.RevenueCatWebhook failed to validate auth header", "header_present", authHeader != "")
		c.JSON(http.StatusForbidden, "Forbidden")
		return
	}

	var webhookEvent RevenueCatWebhookEvent
	if err := c.BindJSON(&webhookEvent); err != nil {
		logger.Errorw("revenuecat.webhook.RevenueCatWebhook failed to bind json", "err", err)
		c.JSON(http.StatusBadRequest, "Invalid JSON")
		return
	}

	// Check if event already processed (idempotency)
	exists, err := h.revenueCatDB.EventExists(c, webhookEvent.Event.ID)
	if err != nil {
		logger.Errorw("revenuecat.webhook.RevenueCatWebhook failed to check event existence", "err", err)
		c.JSON(http.StatusInternalServerError, "Internal Server Error")
		return
	}
	if exists {
		logger.Debugw("revenuecat.webhook.RevenueCatWebhook event already processed", "event_id", webhookEvent.Event.ID)
		c.JSON(http.StatusOK, "Event already processed")
		return
	}

	// Process based on event type
	switch webhookEvent.Event.Type {
	case "INITIAL_PURCHASE":
		h.handleInitialPurchase(c, webhookEvent.Event)
	case "RENEWAL":
		h.handleRenewal(c, webhookEvent.Event)
	case "CANCELLATION":
		h.handleCancellation(c, webhookEvent.Event)

	case "EXPIRATION":
		h.handleExpiration(c, webhookEvent.Event)

	case "BILLING_ISSUE":
		h.handleBillingIssue(c, webhookEvent.Event)

	case "PRODUCT_CHANGE":
		h.handleProductChange(c, webhookEvent.Event)
	case "NON_RENEWING_PURCHASE":
		h.handleNonRenewingPurchase(c, webhookEvent.Event)
	case "TRANSFER":
		h.handleTransfer(c, webhookEvent.Event)
	case "VIRTUAL_CURRENCY_TRANSACTION":
		h.handleVirtualCurrencyTransaction(c, webhookEvent.Event)
	default:
		logger.Debugw("revenuecat.webhook.RevenueCatWebhook unhandled event type", "event_type", webhookEvent.Event.Type)
	}

	// Save event to prevent duplicate processing
	eventModel := &model.RevenueCatEvent{
		EventID:           webhookEvent.Event.ID,
		EventType:         webhookEvent.Event.Type,
		AppUserID:         webhookEvent.Event.AppUserID,
		OriginalAppUserID: webhookEvent.Event.OriginalAppUserID,
		ProductID:         webhookEvent.Event.ProductID,
		Store:             webhookEvent.Event.Store,
		EventTimestamp:    time.Unix(webhookEvent.Event.EventTimestampMs/1000, 0),
		ProcessedAt:       time.Now().UTC(),
	}

	if _, err := h.revenueCatDB.SaveEvent(c, eventModel); err != nil {
		logger.Errorw("revenuecat.webhook.RevenueCatWebhook failed to save event", "err", err)
	}

	c.JSON(http.StatusOK, "OK")
}

func (h *Webhook) handleInitialPurchase(c *gin.Context, event RevenueCatEvent) {
	logger := logging.FromContext(c)

	// Extract user ID from subscriber attributes or app_user_id (assume it's the user ID)
	userID := h.extractUserIDFromEvent(event)
	if userID == 0 {
		logger.Errorw("revenuecat.webhook.handleInitialPurchase failed to extract user ID", "app_user_id", event.AppUserID)
		return
	}

	now := time.Now().UTC()
	subscriptionID := event.AppUserID // Use app_user_id as subscription ID

	if len(event.Transactions) > 0 {
		subscriptionID = event.Transactions[0].OriginalTransactionID
	}

	user, err := h.userRepo.GetUserByID(c, userID)
	if err != nil {
		logger.Errorw("revenuecat.webhook.handleInitialPurchase failed to get user", "err", err)
		return
	}

	subscription := &model.Subscription{
		ID:                     subscriptionID,
		UserID:                 userID,
		CircleID:               user.CircleID,
		Provider:               model.SubscriptionProviderRevenueCat,
		ExternalSubscriptionID: subscriptionID,
		ExternalCustomerID:     event.AppUserID,
		ProductID:              event.ProductID,
		Status:                 "active",
		CreatedAt:              now,
		UpdatedAt:              now,
	}

	// Use event-level expiration if available
	if event.ExpirationAtMs != nil {
		expiresDate := time.Unix(*event.ExpirationAtMs/1000, 0)
		subscription.ExpiresAt = &expiresDate
	}

	// Fallback to transaction data if available
	if len(event.Transactions) > 0 {
		transaction := event.Transactions[0]
		if subscription.ExpiresAt == nil && transaction.ExpiresDateMs != nil {
			expiresDate := time.Unix(*transaction.ExpiresDateMs/1000, 0)
			subscription.ExpiresAt = &expiresDate
		}
	}

	if _, err := h.subscriptionDB.SaveSubscription(c, subscription); err != nil {
		logger.Errorw("revenuecat.webhook.handleInitialPurchase failed to save subscription", "err", err)
	}
}

// extractUserIDFromEvent tries to extract user ID from RevenueCat event
// You may need to adjust this based on how you set up the app_user_id mapping
func (h *Webhook) extractUserIDFromEvent(event RevenueCatEvent) int {
	// Option 1: If app_user_id is the actual user ID
	if userID, err := strconv.Atoi(event.AppUserID); err == nil && userID > 0 {
		return userID
	}

	// Option 2: Check subscriber attributes for $userId
	if event.Subscriber.UserID.Value != "" {
		if userID, err := strconv.Atoi(event.Subscriber.UserID.Value); err == nil && userID > 0 {
			return userID
		}
	}

	return 0
}

func (h *Webhook) handleRenewal(c *gin.Context, event RevenueCatEvent) {
	logger := logging.FromContext(c)

	if len(event.Transactions) == 0 {
		logger.Errorw("revenuecat.webhook.handleRenewal no transactions found")
		return
	}

	transaction := event.Transactions[0]
	subscriptionID := transaction.OriginalTransactionID

	// Check unified subscription table first
	subscription, err := h.subscriptionDB.GetSubscriptionByID(c, subscriptionID)
	if err != nil {
		logger.Errorw("revenuecat.webhook.handleRenewal failed to get subscription", "err", err)
		return
	}

	if subscription == nil {
		// Create new subscription if not found
		h.handleInitialPurchase(c, event)
		return
	}

	// Update subscription
	subscription.Status = "active"
	if transaction.ExpiresDateMs != nil {
		expiresDate := time.Unix(*transaction.ExpiresDateMs/1000, 0)
		subscription.ExpiresAt = &expiresDate
	}

	if err := h.subscriptionDB.UpdateSubscription(c, subscription); err != nil {
		logger.Errorw("revenuecat.webhook.handleRenewal failed to update subscription", "err", err)
	}
}

func (h *Webhook) handleCancellation(c *gin.Context, event RevenueCatEvent) {
	logger := logging.FromContext(c)

	if len(event.Transactions) == 0 {
		logger.Errorw("revenuecat.webhook.handleCancellation no transactions found")
		return
	}

	transaction := event.Transactions[0]
	subscriptionID := transaction.OriginalTransactionID

	subscription, err := h.subscriptionDB.GetSubscriptionByID(c, subscriptionID)
	if err != nil {
		logger.Errorw("revenuecat.webhook.handleCancellation failed to get subscription", "err", err)
		return
	}

	if subscription == nil {
		logger.Errorw("revenuecat.webhook.handleCancellation subscription not found", "transaction_id", transaction.OriginalTransactionID)
		return
	}

	subscription.Status = "cancelled"

	if err := h.subscriptionDB.UpdateSubscription(c, subscription); err != nil {
		logger.Errorw("revenuecat.webhook.handleCancellation failed to update subscription", "err", err)
	}
}

func (h *Webhook) handleExpiration(c *gin.Context, event RevenueCatEvent) {
	logger := logging.FromContext(c)

	if len(event.Transactions) == 0 {
		logger.Errorw("revenuecat.webhook.handleExpiration no transactions found")
		return
	}

	transaction := event.Transactions[0]
	subscriptionID := transaction.OriginalTransactionID

	subscription, err := h.subscriptionDB.GetSubscriptionByID(c, subscriptionID)
	if err != nil {
		logger.Errorw("revenuecat.webhook.handleExpiration failed to get subscription", "err", err)
		return
	}

	if subscription == nil {
		logger.Errorw("revenuecat.webhook.handleExpiration subscription not found", "transaction_id", transaction.OriginalTransactionID)
		return
	}

	subscription.Status = "expired"

	if err := h.subscriptionDB.UpdateSubscription(c, subscription); err != nil {
		logger.Errorw("revenuecat.webhook.handleExpiration failed to update subscription", "err", err)
	}
}

func (h *Webhook) handleBillingIssue(c *gin.Context, event RevenueCatEvent) {
	logger := logging.FromContext(c)

	if len(event.Transactions) == 0 {
		logger.Errorw("revenuecat.webhook.handleBillingIssue no transactions found")
		return
	}

	transaction := event.Transactions[0]
	subscriptionID := transaction.OriginalTransactionID

	subscription, err := h.subscriptionDB.GetSubscriptionByID(c, subscriptionID)
	if err != nil {
		logger.Errorw("revenuecat.webhook.handleBillingIssue failed to get subscription", "err", err)
		return
	}

	if subscription == nil {
		logger.Errorw("revenuecat.webhook.handleBillingIssue subscription not found", "transaction_id", transaction.OriginalTransactionID)
		return
	}

	subscription.Status = "billing_issue"

	if err := h.subscriptionDB.UpdateSubscription(c, subscription); err != nil {
		logger.Errorw("revenuecat.webhook.handleBillingIssue failed to update subscription", "err", err)
	}
}

func (h *Webhook) handleProductChange(c *gin.Context, event RevenueCatEvent) {
	logger := logging.FromContext(c)

	if len(event.Transactions) == 0 {
		logger.Errorw("revenuecat.webhook.handleProductChange no transactions found")
		return
	}

	transaction := event.Transactions[0]
	subscriptionID := transaction.OriginalTransactionID

	subscription, err := h.subscriptionDB.GetSubscriptionByID(c, subscriptionID)
	if err != nil {
		logger.Errorw("revenuecat.webhook.handleProductChange failed to get subscription", "err", err)
		return
	}

	if subscription == nil {
		logger.Errorw("revenuecat.webhook.handleProductChange subscription not found", "transaction_id", transaction.OriginalTransactionID)
		return
	}

	// Update product ID and other relevant fields
	subscription.ProductID = event.ProductID
	if transaction.ExpiresDateMs != nil {
		expiresDate := time.Unix(*transaction.ExpiresDateMs/1000, 0)
		subscription.ExpiresAt = &expiresDate
	}

	if err := h.subscriptionDB.UpdateSubscription(c, subscription); err != nil {
		logger.Errorw("revenuecat.webhook.handleProductChange failed to update subscription", "err", err)
	}
}

func (h *Webhook) handleNonRenewingPurchase(c *gin.Context, event RevenueCatEvent) {
	logger := logging.FromContext(c)
	logger.Debugw("revenuecat.webhook.handleNonRenewingPurchase processing non-renewing purchase", "app_user_id", event.AppUserID, "product_id", event.ProductID)
	// Non-renewing purchases don't create subscriptions, just log the event
}

func (h *Webhook) handleTransfer(c *gin.Context, event RevenueCatEvent) {
	logger := logging.FromContext(c)

	if len(event.Transactions) == 0 {
		logger.Errorw("revenuecat.webhook.handleTransfer no transactions found")
		return
	}

	transaction := event.Transactions[0]
	subscriptionID := transaction.OriginalTransactionID

	subscription, err := h.subscriptionDB.GetSubscriptionByID(c, subscriptionID)
	if err != nil {
		logger.Errorw("revenuecat.webhook.handleTransfer failed to get subscription", "err", err)
		return
	}

	if subscription == nil {
		logger.Errorw("revenuecat.webhook.handleTransfer subscription not found", "transaction_id", transaction.OriginalTransactionID)
		return
	}

	// Update user ID for transfer events
	newUserID := h.extractUserIDFromEvent(event)
	if newUserID > 0 {
		subscription.UserID = newUserID
		subscription.ExternalCustomerID = event.AppUserID
	}

	if err := h.subscriptionDB.UpdateSubscription(c, subscription); err != nil {
		logger.Errorw("revenuecat.webhook.handleTransfer failed to update subscription", "err", err)
	}
}

func (h *Webhook) handleVirtualCurrencyTransaction(c *gin.Context, event RevenueCatEvent) {
	logger := logging.FromContext(c)
	logger.Debugw("revenuecat.webhook.handleVirtualCurrencyTransaction processing virtual currency transaction", "app_user_id", event.AppUserID, "product_id", event.ProductID)
	// Virtual currency transactions are logged but don't affect subscriptions
}

func Webhooks(cfg *config.Config, w *Webhook, r *gin.Engine, auth *jwt.GinJWTMiddleware) {

	paymentsWithoutAuth := r.Group("webhooks")

	paymentsWithoutAuth.Use(authn.LimitRequestBody(authn.MaxAuthRequestBodyBytes))
	paymentsWithoutAuth.Use(utils.TimeoutMiddleware(cfg.Server.WebhookTimeout))
	{
		paymentsWithoutAuth.POST("stripe", w.StripeWebhook)
		paymentsWithoutAuth.POST("revenuecat", w.RevenueCatWebhook)
	}

}
