package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap/zapcore"

	"github.com/spf13/viper"
	"golang.org/x/crypto/hkdf"
)

var (
	Version   = "dev"
	Commit    = "dev"
	BuildDate = "dev"
)

type Config struct {
	DataDir                string              `mapstructure:"data_dir" yaml:"data_dir"`
	Name                   string              `mapstructure:"name" yaml:"name"`
	Telegram               TelegramConfig      `mapstructure:"telegram" yaml:"telegram"`
	Pushover               PushoverConfig      `mapstructure:"pushover" yaml:"pushover"`
	Database               DatabaseConfig      `mapstructure:"database" yaml:"database"`
	Jwt                    JwtConfig           `mapstructure:"jwt" yaml:"jwt"`
	Server                 ServerConfig        `mapstructure:"server" yaml:"server"`
	SchedulerJobs          SchedulerConfig     `mapstructure:"scheduler_jobs" yaml:"scheduler_jobs"`
	EmailConfig            EmailConfig         `mapstructure:"email" yaml:"email"`
	StripeConfig           StripeConfig        `mapstructure:"stripe" yaml:"stripe"`
	RevenueCatConfig       RevenueCatConfig    `mapstructure:"revenuecat" yaml:"revenuecat"`
	IAPConfig              IAPConfig           `mapstructure:"iap" yaml:"iap"`
	OAuth2Config           OAuth2Config        `mapstructure:"oauth2" yaml:"oauth2"`
	WebhookConfig          WebhookConfig       `mapstructure:"webhook" yaml:"webhook"`
	RealTimeConfig         RealTimeConfig      `mapstructure:"realtime" yaml:"realtime"`
	MFAConfig              MFAConfig           `mapstructure:"mfa" yaml:"mfa"`
	Logging                LogConfig           `mapstructure:"logging" yaml:"logging"`
	IsDayDanCloud          bool                `mapstructure:"is_done_tick_dot_com" yaml:"is_done_tick_dot_com"`
	IsUserCreationDisabled bool                `mapstructure:"is_user_creation_disabled" yaml:"is_user_creation_disabled"`
	MinVersion             string              `mapstructure:"min_version" yaml:"min_version"`
	DayDanCloudConfig      DayDanCloudConfig   `mapstructure:"donetick_cloud" yaml:"donetick_cloud"`
	FCM                    FCMConfig           `mapstructure:"fcm" yaml:"fcm"`
	FeatureLimits          FeatureLimitsConfig `mapstructure:"feature_limits" yaml:"feature_limits"`
	Storage                StorageConfig       `mapstructure:"storage" yaml:"storage"`
	SingleCircleInstance   bool                `mapstructure:"single_circle_instance" yaml:"single_circle_instance"`
	DisablePasswordAuth    bool                `mapstructure:"disable_password_auth" yaml:"disable_password_auth"`
	Info                   Info
}

type Info struct {
	Version   string
	Commit    string
	BuildDate string
}
type StorageConfig struct {
	StorageType string `mapstructure:"storage_type" yaml:"storage_type"`
	// CloudStorage:
	BucketName     string `mapstructure:"bucket_name" yaml:"bucket_name"`
	Region         string `mapstructure:"region" yaml:"region"`
	BasePath       string `mapstructure:"base_path" yaml:"base_path"`
	AccessKey      string `mapstructure:"access_key" yaml:"access_key"`
	SecretKey      string `mapstructure:"secret_key" yaml:"secret_key"`
	Endpoint       string `mapstructure:"endpoint" yaml:"endpoint"`
	MaxUserStorage int    `mapstructure:"max_user_storage" yaml:"max_user_storage"`
	MaxFileSize    int64  `mapstructure:"max_file_size" yaml:"max_file_size"`
	PublicHost     string `mapstructure:"public_host" yaml:"public_host"`
	// PublicRead returns unsigned URLs instead of SigV4 presigned URLs.
	// The bucket/prefix must be configured for public anonymous reads
	// (e.g. via a bucket policy). Intended for self-hosted deployments
	// that don't want the 7-day presigned URL ceiling.
	PublicRead bool `mapstructure:"public_read" yaml:"public_read"`
	// PublicBucketName is an optional separate bucket for publicly readable
	// assets (e.g. profile photos). If empty, public uploads fall back to the
	// private bucket (BucketName).
	PublicBucketName string `mapstructure:"public_bucket_name" yaml:"public_bucket_name"`
	// PublicBucketHost is the CDN / custom-domain host used to construct bare
	// (unsigned) URLs for objects in PublicBucketName. Example: "pub.donetick.com".
	// If empty, GetPublicURL falls back to a presigned URL from the private bucket.
	PublicBucketHost string `mapstructure:"public_bucket_host" yaml:"public_bucket_host"`
	// PathStyle forces path-style S3 addressing (endpoint/bucket/key)
	// instead of the default virtual-hosted style (bucket.endpoint/key).
	// Required for S3-compatible backends like MinIO that don't expose
	// a DNS wildcard at `*.<endpoint-host>`, as well as for plain HTTP
	// endpoints without matching TLS wildcard certs.
	PathStyle bool `mapstructure:"path_style" yaml:"path_style"`
	// MediaSecret is the independent HMAC key used to sign locally-served
	// media URLs. It is deliberately separate from jwt.secret so a media-URL
	// credential can never be confused with (or mint) a session JWT, and so
	// each credential family rotates independently. When unset it is derived
	// from jwt.secret via HKDF with storage-specific domain separation —
	// zero-config installs keep working, but the derived key still cannot be
	// swapped with the JWT key in either direction. Rotate by setting this
	// value (or media_secret_file) to something new; rotating only invalidates
	// outstanding signed media URLs (clients transparently re-sign), never
	// login sessions.
	MediaSecret string `mapstructure:"media_secret" yaml:"media_secret"`
	// MediaSecretFile is an alternative to media_secret: a file containing
	// the media signing secret. Created with random content on first boot if
	// missing, mirroring jwt.secret_file semantics.
	MediaSecretFile string `mapstructure:"media_secret_file" yaml:"media_secret_file"`
	// SignedURLTTL is the lifetime minted into signed media URLs (local and
	// S3 presigned). It is capped at 7 days because AWS SigV4 refuses longer
	// presignatures; shorter values are safe because clients re-sign via
	// /api/v1/files/sign whenever a URL is expired.
	SignedURLTTL time.Duration `mapstructure:"signed_url_ttl" yaml:"signed_url_ttl"`
	// S3OperationTimeout is the per-operation deadline the storage client
	// applies to every S3 API call (uploads, deletes), including SDK
	// retries. It bounds stalled S3/MinIO endpoints; caller cancellation
	// still wins immediately. Zero means the built-in default (2 minutes).
	S3OperationTimeout time.Duration `mapstructure:"s3_operation_timeout" yaml:"s3_operation_timeout"`
	// AllowLegacyMediaSignatures keeps accepting media URLs signed directly
	// with the raw JWT secret (the pre-phase-6 scheme). This is a bounded
	// compatibility window: such URLs already expire at most 7 days after
	// they were minted, and the flag exists only so an upgrade does not
	// break clients holding pre-upgrade URLs mid-session. It can be set to
	// false immediately; new deployments get no benefit from enabling it.
	AllowLegacyMediaSignatures bool `mapstructure:"allow_legacy_media_signatures" yaml:"allow_legacy_media_signatures"`
}
type DayDanCloudConfig struct {
	GoogleClientID        string `mapstructure:"google_client_id" yaml:"google_client_id"`
	GoogleAndroidClientID string `mapstructure:"google_android_client_id" yaml:"google_android_client_id"`
	GoogleIOSClientID     string `mapstructure:"google_ios_client_id" yaml:"google_ios_client_id"`
	AppleClientID         string `mapstructure:"apple_client_id" yaml:"apple_client_id"`
}

type FeatureLimitsConfig struct {
	MaxCircleMembers     int `mapstructure:"max_circle_members" yaml:"max_circle_members" default:"2"`
	PlusCircleMaxMembers int `mapstructure:"plus_circle_max_members" yaml:"plus_circle_max_members" default:"6"`
	MaxSubaccounts       int `mapstructure:"max_subaccounts" yaml:"max_subaccounts" default:"1"`
	PlusMaxSubaccounts   int `mapstructure:"plus_max_subaccounts" yaml:"plus_max_subaccounts" default:"5"`
}

type TelegramConfig struct {
	Token string `mapstructure:"token" yaml:"token"`
}

type PushoverConfig struct {
	Token string `mapstructure:"token" yaml:"token"`
}

type DatabaseConfig struct {
	Type      string `mapstructure:"type" yaml:"type" default:"sqlite"`
	Path      string `mapstructure:"path" yaml:"path"`
	Host      string `mapstructure:"host" yaml:"host"`
	Port      int    `mapstructure:"port" yaml:"port"`
	User      string `mapstructure:"user" yaml:"user"`
	Password  string `mapstructure:"password" yaml:"password"`
	Name      string `mapstructure:"name" yaml:"name"`
	Migration bool   `mapstructure:"migration" yaml:"migration" default:"true"`
	LogLevel  int    `mapstructure:"logger" yaml:"logger"`
	// Connection-pool bounds. Zero/negative values fall back to built-in
	// defaults (25 open / 5 idle, 1h max lifetime, 10m max idle time); they
	// exist so operators can size pools for PostgreSQL max_connections.
	MaxOpenConns    int           `mapstructure:"max_open_conns" yaml:"max_open_conns"`
	MaxIdleConns    int           `mapstructure:"max_idle_conns" yaml:"max_idle_conns"`
	ConnMaxLifetime time.Duration `mapstructure:"conn_max_lifetime" yaml:"conn_max_lifetime"`
	ConnMaxIdleTime time.Duration `mapstructure:"conn_max_idle_time" yaml:"conn_max_idle_time"`
}

type JwtConfig struct {
	Secret      string        `mapstructure:"secret" yaml:"secret"`
	SecretFile  string        `mapstructure:"secret_file" yaml:"secret_file"`
	SessionTime time.Duration `mapstructure:"session_time" yaml:"session_time" default:"24h"` // 24 hours
	MaxRefresh  time.Duration `mapstructure:"max_refresh" yaml:"max_refresh" default:"1440h"` // 60 days
	// AccessTokenTime is the lifetime of the short-lived access JWTs minted by
	// the token service for interactive sessions (browser PWA and native app).
	// It is deliberately separate from SessionTime, which only governs legacy
	// /api/v1/auth/login/legacy tokens kept for API/integration compatibility.
	// Browsers restore sessions transparently via the HttpOnly refresh cookie,
	// so a short access-token lifetime costs no UX while shrinking the window
	// in which a stolen access JWT outlives logout/session revocation.
	AccessTokenTime time.Duration `mapstructure:"access_token_time" yaml:"access_token_time" default:"15m"`
}

type ServerConfig struct {
	Port             int           `mapstructure:"port" yaml:"port" default:"2021"`
	RatePeriod       time.Duration `mapstructure:"rate_period" yaml:"rate_period" default:"60s"`
	RateLimit        int           `mapstructure:"rate_limit" yaml:"rate_limit" default:"300"`
	ReadTimeout      time.Duration `mapstructure:"read_timeout" yaml:"read_timeout" default:"5s"`
	WriteTimeout     time.Duration `mapstructure:"write_timeout" yaml:"write_timeout" default:"5s"`
	WebhookTimeout   time.Duration `mapstructure:"webhook_timeout" yaml:"webhook_timeout" default:"5s"`
	CorsAllowOrigins []string      `mapstructure:"cors_allow_origins" yaml:"cors_allow_origins"`
	ServeFrontend    bool          `mapstructure:"serve_frontend" yaml:"serve_frontend"`
	ServeSwagger     bool          `mapstructure:"serve_swagger" yaml:"serve_swagger"`
	PublicHost       string        `mapstructure:"public_host" yaml:"public_host" default:"http://localhost:2021"`

	// Dedicated budget for password changes (per IP, per period). Tighter than
	// the general API bucket so the current-password check cannot be
	// brute-forced from a fast connection.
	RateLimitPasswordChangePeriod time.Duration `mapstructure:"rate_limit_password_change_period" yaml:"rate_limit_password_change_period" default:"60s"`
	RateLimitPasswordChange       int           `mapstructure:"rate_limit_password_change" yaml:"rate_limit_password_change" default:"10"`
	RateLimitAuthPeriod           time.Duration `mapstructure:"rate_limit_auth_period" yaml:"rate_limit_auth_period" default:"60s"`
	RateLimitAuth                 int           `mapstructure:"rate_limit_auth" yaml:"rate_limit_auth" default:"10"`
	RateLimitRecoveryPeriod       time.Duration `mapstructure:"rate_limit_recovery_period" yaml:"rate_limit_recovery_period" default:"1h"`
	RateLimitRecovery             int           `mapstructure:"rate_limit_recovery" yaml:"rate_limit_recovery" default:"5"`
	RateLimitMaxActiveBuckets     int           `mapstructure:"rate_limit_max_active_buckets" yaml:"rate_limit_max_active_buckets" default:"100000"`

	// TrustedProxies lists the proxies allowed to set forwarding headers
	// (X-Forwarded-Proto, X-Forwarded-Host, X-Forwarded-For). Entries are IPs
	// or CIDRs. Forwarding headers from any other client are ignored, so a
	// directly exposed instance cannot be fooled into marking requests as
	// HTTPS or deriving CSRF origins from attacker-chosen hosts. The default
	// covers loopback, which matches the common single-host nginx/Caddy setup.
	TrustedProxies []string `mapstructure:"trusted_proxies" yaml:"trusted_proxies"`
}

type SchedulerConfig struct {
	DueJob     time.Duration `mapstructure:"due_job" yaml:"due_job"`
	OverdueJob time.Duration `mapstructure:"overdue_job" yaml:"overdue_job"`
	PreDueJob  time.Duration `mapstructure:"pre_due_job" yaml:"pre_due_job"`
}

type StripeConfig struct {
	APIKey         string         `mapstructure:"api_key"`
	WhitelistedIPs []string       `mapstructure:"whitelisted_ips"`
	Prices         []StripePrices `mapstructure:"prices"`
	SuccessURL     string         `mapstructure:"success_url"`
	CancelURL      string         `mapstructure:"cancel_url"`
	// WebhookSecret is the signing secret of the Stripe webhook endpoint
	// (whsec_...). It is required for /webhooks/stripe to accept any event:
	// without it the endpoint fails closed (503) instead of trusting the
	// legacy IP allowlist alone. IP restrictions remain as optional
	// defense-in-depth when configured.
	WebhookSecret string `mapstructure:"webhook_secret" yaml:"webhook_secret"`
	// WebhookSecretFile is an alternative to webhook_secret: a file holding
	// the endpoint secret, mirroring jwt.secret_file semantics.
	WebhookSecretFile string `mapstructure:"webhook_secret_file" yaml:"webhook_secret_file"`
	// WebhookTolerance is the maximum accepted age/future-skew of a signed
	// Stripe event. Defaults to Stripe's 5-minute recommendation; values
	// above 24h are rejected at startup.
	WebhookTolerance time.Duration `mapstructure:"webhook_tolerance" yaml:"webhook_tolerance"`
}

type StripePrices struct {
	PriceID string `mapstructure:"id"`
	Name    string `mapstructure:"name"`
}

type RevenueCatConfig struct {
	AuthSecret       string   `mapstructure:"auth_secret" yaml:"auth_secret"`
	WhitelistedIPs   []string `mapstructure:"whitelisted_ips" yaml:"whitelisted_ips"`
	SharedSecret     string   `mapstructure:"shared_secret" yaml:"shared_secret"`
	EnableValidation bool     `mapstructure:"enable_validation" yaml:"enable_validation"`
}

type IAPConfig struct {
	Apple  AppleIAPConfig  `mapstructure:"apple" yaml:"apple"`
	Google GoogleIAPConfig `mapstructure:"google" yaml:"google"`
}

type AppleIAPConfig struct {
	BundleID     string `mapstructure:"bundle_id" yaml:"bundle_id"`
	SharedSecret string `mapstructure:"shared_secret" yaml:"shared_secret"`
	Sandbox      bool   `mapstructure:"sandbox" yaml:"sandbox"`
	KeyID        string `mapstructure:"key_id" yaml:"key_id"`
	IssuerID     string `mapstructure:"issuer_id" yaml:"issuer_id"`
	PrivateKey   string `mapstructure:"private_key" yaml:"private_key"`
}

type GoogleIAPConfig struct {
	PackageName        string `mapstructure:"package_name" yaml:"package_name"`
	ServiceAccountJSON string `mapstructure:"service_account_json" yaml:"service_account_json"`
}

type FCMConfig struct {
	CredentialsPath string `json:"credentials_path" mapstructure:"credentials_path"`
	ProjectID       string `json:"project_id" mapstructure:"project_id"`
}
type EmailConfig struct {
	Email     string `mapstructure:"email"`
	User      string `mapstructure:"user"`
	Key       string `mapstructure:"key"`
	Host      string `mapstructure:"host"`
	Port      int    `mapstructure:"port"`
	AppHost   string `mapstructure:"app_host" yaml:"app_host"`
	LogRawURL bool   `mapstructure:"log_raw_url" yaml:"log_raw_url"`
	// Provider selects which email sender implementation to use.
	// "smtp" (default, empty also means smtp) uses host/port/user/key as SMTP creds.
	// "smtp2go" uses the SMTP2Go HTTP API with `key` as the API key and the
	// template IDs below.
	Provider                string `mapstructure:"provider" yaml:"provider" default:"smtp"`
	VerifyTemplateID        string `mapstructure:"verify_template_id" yaml:"verify_template_id"`
	ResetPasswordTemplateID string `mapstructure:"reset_password_template_id" yaml:"reset_password_template_id"`
}

type OAuth2Config struct {
	ClientID            string   `mapstructure:"client_id" yaml:"client_id"`
	ClientSecret        string   `mapstructure:"client_secret" yaml:"client_secret"`
	RedirectURL         string   `mapstructure:"redirect_url" yaml:"redirect_url"`
	AllowedRedirectURLs []string `mapstructure:"allowed_redirect_urls" yaml:"allowed_redirect_urls"`
	Scopes              []string `mapstructure:"scopes" yaml:"scopes"`
	AuthURL             string   `mapstructure:"auth_url" yaml:"auth_url"`
	TokenURL            string   `mapstructure:"token_url" yaml:"token_url"`
	UserInfoURL         string   `mapstructure:"user_info_url" yaml:"user_info_url"`
	Name                string   `mapstructure:"name" yaml:"name"`
	AdminGroups         []string `mapstructure:"admin_groups" yaml:"admin_groups"`
	ManagerGroups       []string `mapstructure:"manager_groups" yaml:"manager_groups"`
	// AllowLegacyOAuthState permits OAuth2 callbacks whose state has no
	// matching server-side pending transaction (clients predating the
	// server-enforced state/PKCE flow, e.g. un-updated native apps). This
	// weakens CSRF/replay protection and should only be enabled transiently.
	AllowLegacyOAuthState bool `mapstructure:"allow_legacy_oauth_state" yaml:"allow_legacy_oauth_state"`
}

type WebhookConfig struct {
	Timeout   time.Duration `mapstructure:"timeout" yaml:"timeout" default:"5s"`
	QueueSize int           `mapstructure:"queue_size" yaml:"queue_size" default:"100"`
	// AllowPrivateNetworks permits circle/notification webhook destinations
	// on loopback/RFC1918/link-local addresses. Off by default because an
	// authenticated circle admin must not be able to pivot server requests
	// into internal infrastructure (SSRF); self-hosted deployments whose
	// consumers legitimately live on the LAN can enable it explicitly.
	AllowPrivateNetworks bool `mapstructure:"allow_private_networks" yaml:"allow_private_networks"`
	// Workers bounds global webhook delivery concurrency across all tenants
	// (destinations). Each destination additionally has at most one delivery
	// in flight, so a slow tenant cannot consume more than its own slot while
	// other tenants share the remaining workers.
	Workers int `mapstructure:"workers" yaml:"workers" default:"8"`
	// MaxAttempts is the bounded number of delivery attempts per webhook
	// event (1 = no retries).
	MaxAttempts int `mapstructure:"max_attempts" yaml:"max_attempts" default:"4"`
	// BackoffInitial is the base delay for exponential backoff between retry
	// attempts. Actual delays add ±20% jitter and are capped at BackoffMax.
	BackoffInitial time.Duration `mapstructure:"backoff_initial" yaml:"backoff_initial" default:"1s"`
	BackoffMax     time.Duration `mapstructure:"backoff_max" yaml:"backoff_max" default:"30s"`
	// SigningSecret is the HMAC-SHA256 key used to sign every outbound
	// DayDan-generated webhook payload. It is deliberately separate from
	// jwt.secret and storage.media_secret; when unset it is auto-created as
	// a random secret file (or HKDF-derived from jwt.secret when no data dir
	// is writable — see resolveWebhookSigningKey). Receivers verify with
	// docs/webhook-signing.md.
	SigningSecret     string `mapstructure:"signing_secret" yaml:"signing_secret"`
	SigningSecretFile string `mapstructure:"signing_secret_file" yaml:"signing_secret_file"`
}

type RealTimeConfig struct {
	Enabled               bool          `mapstructure:"enabled" yaml:"enabled" default:"true"`
	WebSocketEnabled      bool          `mapstructure:"websocket_enabled" yaml:"websocket_enabled" default:"true"`
	SSEEnabled            bool          `mapstructure:"sse_enabled" yaml:"sse_enabled" default:"true"`
	HeartbeatInterval     time.Duration `mapstructure:"heartbeat_interval" yaml:"heartbeat_interval" default:"30s"`
	ConnectionTimeout     time.Duration `mapstructure:"connection_timeout" yaml:"connection_timeout" default:"60s"`
	MinConnectionInterval time.Duration `mapstructure:"min_connection_interval" yaml:"min_connection_interval" default:"5s"`
	MaxConnections        int           `mapstructure:"max_connections" yaml:"max_connections" default:"1000"`
	MaxConnectionsPerUser int           `mapstructure:"max_connections_per_user" yaml:"max_connections_per_user" default:"5"`
	EventQueueSize        int           `mapstructure:"event_queue_size" yaml:"event_queue_size" default:"2048"`
	CleanupInterval       time.Duration `mapstructure:"cleanup_interval" yaml:"cleanup_interval" default:"2m"`
	StaleThreshold        time.Duration `mapstructure:"stale_threshold" yaml:"stale_threshold" default:"5m"`
	EnableCompression     bool          `mapstructure:"enable_compression" yaml:"enable_compression" default:"true"`
	EnableStats           bool          `mapstructure:"enable_stats" yaml:"enable_stats" default:"true"`
	AllowedOrigins        []string      `mapstructure:"allowed_origins" yaml:"allowed_origins"`
	MaxSSETickets         int           `mapstructure:"max_sse_tickets" yaml:"max_sse_tickets" default:"10000"`
}

type MFAConfig struct {
	Enabled                 bool          `mapstructure:"enabled" yaml:"enabled" default:"true"`
	SessionTimeoutMinutes   int           `mapstructure:"session_timeout_minutes" yaml:"session_timeout_minutes" default:"15"`
	BackupCodeCount         int           `mapstructure:"backup_code_count" yaml:"backup_code_count" default:"10"`
	MaxVerificationAttempts int           `mapstructure:"max_verification_attempts" yaml:"max_verification_attempts" default:"5"`
	RateLimitWindow         time.Duration `mapstructure:"rate_limit_window" yaml:"rate_limit_window" default:"5m"`
}

type LogConfig struct {
	Level       string `mapstructure:"level" yaml:"level" default:"info"`
	Encoding    string `mapstructure:"encoding" yaml:"encoding" default:"console"`
	Development bool   `mapstructure:"development" yaml:"development" default:"false"`
}

func NewConfig() *Config {
	return &Config{
		DataDir: "data",
		Name:    "daydan",
		Database: DatabaseConfig{
			Type:      "sqlite",
			Migration: true,
		},
		Jwt: JwtConfig{
			SessionTime:     24 * time.Hour,
			MaxRefresh:      60 * 24 * time.Hour,
			AccessTokenTime: 15 * time.Minute,
		},
		Server: ServerConfig{
			Port:             2021,
			RatePeriod:       60 * time.Second,
			RateLimit:        300,
			ReadTimeout:      10 * time.Second,
			WriteTimeout:     10 * time.Second,
			WebhookTimeout:   5 * time.Second,
			CorsAllowOrigins: []string{"http://localhost:5173", "http://localhost:7926", "http://localhost", "https://localhost", "capacitor://localhost"},
			ServeFrontend:    true,

			RateLimitPasswordChangePeriod: 60 * time.Second,
			RateLimitPasswordChange:       10,
			RateLimitAuthPeriod:           60 * time.Second,
			RateLimitAuth:                 10,
			RateLimitRecoveryPeriod:       time.Hour,
			RateLimitRecovery:             5,
			RateLimitMaxActiveBuckets:     100000,
			TrustedProxies:                []string{"127.0.0.1/8", "::1/128"},
		},
		SchedulerJobs: SchedulerConfig{
			DueJob:     30 * time.Minute,
			OverdueJob: 3 * time.Hour,
			PreDueJob:  3 * time.Hour,
		},
		RealTimeConfig: RealTimeConfig{
			Enabled:               true,
			WebSocketEnabled:      true,
			SSEEnabled:            true,
			HeartbeatInterval:     30 * time.Second,
			ConnectionTimeout:     60 * time.Second,
			MinConnectionInterval: 5 * time.Second,
			MaxConnections:        1000,
			MaxConnectionsPerUser: 5,
			EventQueueSize:        2048,
			CleanupInterval:       2 * time.Minute,
			StaleThreshold:        5 * time.Minute,
			EnableCompression:     true,
			EnableStats:           true,
			AllowedOrigins:        []string{"http://localhost:5173", "http://localhost:7926", "http://localhost", "https://localhost", "capacitor://localhost"},
			MaxSSETickets:         10000,
		},
		WebhookConfig: WebhookConfig{
			Timeout:        5 * time.Second,
			QueueSize:      100,
			Workers:        8,
			MaxAttempts:    4,
			BackoffInitial: time.Second,
			BackoffMax:     30 * time.Second,
		},
		MFAConfig: MFAConfig{
			Enabled:                 true,
			SessionTimeoutMinutes:   15,
			BackupCodeCount:         10,
			MaxVerificationAttempts: 5,
			RateLimitWindow:         5 * time.Minute,
		},
		Logging: LogConfig{
			Level:       "info",
			Encoding:    "console",
			Development: false,
		},
		Storage: StorageConfig{
			StorageType:                "local",
			MaxUserStorage:             10 * 1024 * 1024 * 1024,
			MaxFileSize:                10 * 1024 * 1024,
			SignedURLTTL:               MaxSignedURLTTL,
			AllowLegacyMediaSignatures: true,
		},
		FeatureLimits: FeatureLimitsConfig{
			MaxCircleMembers:     2,
			PlusCircleMaxMembers: 6,
			MaxSubaccounts:       1,
			PlusMaxSubaccounts:   5,
		},
	}
}

func configEnvironmentOverrides(config *Config) {
	if token := envTelegramToken(); token != "" {
		config.Telegram.Token = token
	}
	if token := envPushoverToken(); token != "" {
		config.Pushover.Token = token
	}
	if environmentIsTrue("DAYDAN_DISABLE_SIGNUP", "DONETICK_DISABLE_SIGNUP") {
		config.IsUserCreationDisabled = true
	}
	if environmentIsTrue("DAYDAN_SINGLE_CIRCLE_INSTANCE", "DT_SINGLE_CIRCLE_INSTANCE") {
		config.SingleCircleInstance = true
	}
	if environmentIsTrue("DAYDAN_DISABLE_PASSWORD_AUTH", "DT_DISABLE_PASSWORD_AUTH") {
		config.DisablePasswordAuth = true
	}
	if raw := firstEnvironment("DAYDAN_OAUTH2_ADMIN_GROUPS", "DT_OAUTH2_ADMIN_GROUPS"); raw != "" {
		config.OAuth2Config.AdminGroups = splitEnvironmentList(raw)
	}
	if raw := firstEnvironment("DAYDAN_OAUTH2_MANAGER_GROUPS", "DT_OAUTH2_MANAGER_GROUPS"); raw != "" {
		config.OAuth2Config.ManagerGroups = splitEnvironmentList(raw)
	}

	if envAllowLegacyOAuthState() {
		config.OAuth2Config.AllowLegacyOAuthState = true
	}

	// Logging environment overrides
	if level := envLogLevel(); level != "" {
		config.Logging.Level = level
	}
	if encoding := envLogEncoding(); encoding != "" {
		config.Logging.Encoding = encoding
	}
	if environmentIsTrue("DAYDAN_LOG_DEVELOPMENT", "DONETICK_LOG_DEVELOPMENT") {
		config.Logging.Development = true
	}

	// Trusted proxies come in as a comma-separated list because Viper does not
	// split raw environment values into slices on its own.
	if raw := firstEnvironment("DAYDAN_TRUSTED_PROXIES", "DT_SERVER_TRUSTED_PROXIES", "TASKIER_TRUSTED_PROXIES"); raw != "" {
		var proxies []string
		for _, part := range strings.Split(raw, ",") {
			if trimmed := strings.TrimSpace(part); trimmed != "" {
				proxies = append(proxies, trimmed)
			}
		}
		config.Server.TrustedProxies = proxies
	}
}

func splitEnvironmentList(raw string) []string {
	var values []string
	for _, part := range strings.Split(raw, ",") {
		if value := strings.TrimSpace(part); value != "" {
			values = append(values, value)
		}
	}
	return values
}
func setDayDanDefaults(v *viper.Viper) {
	cfg := NewConfig()
	v.SetDefault("name", cfg.Name)
	v.SetDefault("data_dir", cfg.DataDir)
	v.SetDefault("is_done_tick_dot_com", false)
	v.SetDefault("is_user_creation_disabled", false)
	v.SetDefault("database.type", cfg.Database.Type)
	v.SetDefault("database.migration", cfg.Database.Migration)
	v.SetDefault("database.max_open_conns", 0)
	v.SetDefault("database.max_idle_conns", 0)
	v.SetDefault("database.conn_max_lifetime", 0)
	v.SetDefault("database.conn_max_idle_time", 0)
	v.SetDefault("jwt.session_time", cfg.Jwt.SessionTime)
	v.SetDefault("jwt.max_refresh", cfg.Jwt.MaxRefresh)
	v.SetDefault("jwt.access_token_time", cfg.Jwt.AccessTokenTime)
	v.SetDefault("server.port", cfg.Server.Port)
	v.SetDefault("server.rate_period", cfg.Server.RatePeriod)
	v.SetDefault("server.rate_limit", cfg.Server.RateLimit)
	v.SetDefault("server.rate_limit_password_change_period", cfg.Server.RateLimitPasswordChangePeriod)
	v.SetDefault("server.rate_limit_password_change", cfg.Server.RateLimitPasswordChange)
	v.SetDefault("server.rate_limit_auth_period", cfg.Server.RateLimitAuthPeriod)
	v.SetDefault("server.rate_limit_auth", cfg.Server.RateLimitAuth)
	v.SetDefault("server.rate_limit_recovery_period", cfg.Server.RateLimitRecoveryPeriod)
	v.SetDefault("server.rate_limit_recovery", cfg.Server.RateLimitRecovery)
	v.SetDefault("server.rate_limit_max_active_buckets", cfg.Server.RateLimitMaxActiveBuckets)
	v.SetDefault("server.read_timeout", cfg.Server.ReadTimeout)
	v.SetDefault("server.write_timeout", cfg.Server.WriteTimeout)
	v.SetDefault("server.webhook_timeout", cfg.Server.WebhookTimeout)
	v.SetDefault("server.cors_allow_origins", cfg.Server.CorsAllowOrigins)
	v.SetDefault("server.trusted_proxies", cfg.Server.TrustedProxies)
	v.SetDefault("server.serve_frontend", cfg.Server.ServeFrontend)
	v.SetDefault("server.serve_swagger", false)
	v.SetDefault("scheduler_jobs.due_job", cfg.SchedulerJobs.DueJob)
	v.SetDefault("scheduler_jobs.overdue_job", cfg.SchedulerJobs.OverdueJob)
	v.SetDefault("scheduler_jobs.pre_due_job", cfg.SchedulerJobs.PreDueJob)
	v.SetDefault("storage.storage_type", cfg.Storage.StorageType)
	v.SetDefault("storage.max_user_storage", cfg.Storage.MaxUserStorage)
	v.SetDefault("storage.max_file_size", cfg.Storage.MaxFileSize)
	v.SetDefault("storage.signed_url_ttl", cfg.Storage.SignedURLTTL)
	v.SetDefault("storage.s3_operation_timeout", 0)
	v.SetDefault("storage.allow_legacy_media_signatures", cfg.Storage.AllowLegacyMediaSignatures)
	v.SetDefault("realtime.enabled", cfg.RealTimeConfig.Enabled)
	v.SetDefault("realtime.websocket_enabled", cfg.RealTimeConfig.WebSocketEnabled)
	v.SetDefault("realtime.sse_enabled", cfg.RealTimeConfig.SSEEnabled)
	v.SetDefault("realtime.heartbeat_interval", cfg.RealTimeConfig.HeartbeatInterval)
	v.SetDefault("realtime.connection_timeout", cfg.RealTimeConfig.ConnectionTimeout)
	v.SetDefault("realtime.min_connection_interval", cfg.RealTimeConfig.MinConnectionInterval)
	v.SetDefault("realtime.max_connections", cfg.RealTimeConfig.MaxConnections)
	v.SetDefault("realtime.max_connections_per_user", cfg.RealTimeConfig.MaxConnectionsPerUser)
	v.SetDefault("realtime.event_queue_size", cfg.RealTimeConfig.EventQueueSize)
	v.SetDefault("realtime.cleanup_interval", cfg.RealTimeConfig.CleanupInterval)
	v.SetDefault("realtime.stale_threshold", cfg.RealTimeConfig.StaleThreshold)
	v.SetDefault("realtime.enable_compression", cfg.RealTimeConfig.EnableCompression)
	v.SetDefault("realtime.enable_stats", cfg.RealTimeConfig.EnableStats)
	v.SetDefault("realtime.allowed_origins", cfg.RealTimeConfig.AllowedOrigins)
	v.SetDefault("realtime.max_sse_tickets", cfg.RealTimeConfig.MaxSSETickets)
	v.SetDefault("logging.level", cfg.Logging.Level)
	v.SetDefault("logging.encoding", cfg.Logging.Encoding)
	v.SetDefault("logging.development", cfg.Logging.Development)
	v.SetDefault("webhook.timeout", cfg.WebhookConfig.Timeout)
	v.SetDefault("webhook.queue_size", cfg.WebhookConfig.QueueSize)
	v.SetDefault("webhook.allow_private_networks", cfg.WebhookConfig.AllowPrivateNetworks)
	v.SetDefault("webhook.workers", cfg.WebhookConfig.Workers)
	v.SetDefault("webhook.max_attempts", cfg.WebhookConfig.MaxAttempts)
	v.SetDefault("webhook.backoff_initial", cfg.WebhookConfig.BackoffInitial)
	v.SetDefault("webhook.backoff_max", cfg.WebhookConfig.BackoffMax)
	v.SetDefault("mfa.enabled", cfg.MFAConfig.Enabled)
	v.SetDefault("mfa.session_timeout_minutes", cfg.MFAConfig.SessionTimeoutMinutes)
	v.SetDefault("mfa.backup_code_count", cfg.MFAConfig.BackupCodeCount)
	v.SetDefault("mfa.max_verification_attempts", cfg.MFAConfig.MaxVerificationAttempts)
	v.SetDefault("mfa.rate_limit_window", cfg.MFAConfig.RateLimitWindow)
	v.SetDefault("feature_limits.max_circle_members", cfg.FeatureLimits.MaxCircleMembers)
	v.SetDefault("feature_limits.plus_circle_max_members", cfg.FeatureLimits.PlusCircleMaxMembers)
	v.SetDefault("feature_limits.max_subaccounts", cfg.FeatureLimits.MaxSubaccounts)
	v.SetDefault("feature_limits.plus_max_subaccounts", cfg.FeatureLimits.PlusMaxSubaccounts)
}

func LoadConfig() *Config {
	cfg, err := LoadConfigE()
	if err != nil {
		panic(fmt.Sprintf("DayDan configuration error: %v", err))
	}
	return cfg
}

func applyDayDanCloudConfigAliases(v *viper.Viper, config *Config) error {
	if v.IsSet("is_daydan_cloud") {
		config.IsDayDanCloud = v.GetBool("is_daydan_cloud")
	}
	if v.IsSet("daydan_cloud") {
		var alias DayDanCloudConfig
		if err := v.UnmarshalKey("daydan_cloud", &alias); err != nil {
			return fmt.Errorf("decode daydan_cloud alias: %w", err)
		}
		config.DayDanCloudConfig = alias
	}
	return nil
}

func readOptionalConfigFile(v *viper.Viper) error {
	configFile := envConfigFile()
	if configFile != "" {
		v.SetConfigFile(configFile)
		if err := v.ReadInConfig(); err != nil {
			return fmt.Errorf("read config: %w", err)
		}
		return nil
	}

	if profile := envProfile(); profile != "" {
		v.SetConfigName(profile)
		if err := v.ReadInConfig(); err != nil {
			var configNotFoundError viper.ConfigFileNotFoundError
			if !errors.As(err, &configNotFoundError) {
				return fmt.Errorf("read optional config: %w", err)
			}
		}
		return nil
	}

	// Prefer daydan.{yaml,yml}; fall back to legacy taskier.{yaml,yml}.
	if err := readNamedConfigFile(v, "daydan"); err != nil {
		var configNotFoundError viper.ConfigFileNotFoundError
		if !errors.As(err, &configNotFoundError) {
			return fmt.Errorf("read daydan config: %w", err)
		}
		if err := readNamedConfigFile(v, "taskier"); err != nil {
			if !errors.As(err, &configNotFoundError) {
				return fmt.Errorf("read taskier config: %w", err)
			}
		}
	}
	return nil
}

func readNamedConfigFile(v *viper.Viper, name string) error {
	v.SetConfigName(name)
	return v.ReadInConfig()
}

func LoadConfigE() (*Config, error) {
	// https://github.com/spf13/viper/issues/1895#issuecomment-3316091229
	v := viper.NewWithOptions(viper.ExperimentalBindStruct())
	setDayDanDefaults(v)

	v.SetEnvPrefix("DT")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	v.AddConfigPath("./config")
	v.AddConfigPath("/config")
	v.AddConfigPath(".")
	// Config type is inferred from the file extension. Do not call
	// SetConfigType here: viper treats a non-empty config type as permission to
	// match a config file with no extension, which collides with the "/daydan"
	// binary present in the container's working directory and panics parsing it
	// as YAML.

	if err := readOptionalConfigFile(v); err != nil {
		return nil, err
	}
	v.AutomaticEnv()
	if value := envDataDir(); value != "" {
		v.Set("data_dir", value)
	}
	if value := envJWTSecret(); value != "" {
		v.Set("jwt.secret", value)
	}
	if value := envStorageMediaSecret(); value != "" {
		v.Set("storage.media_secret", value)
	}
	if value := envSQLitePath(); value != "" {
		v.Set("database.path", value)
	}

	var config Config
	err := v.Unmarshal(&config)
	if err != nil {
		return nil, fmt.Errorf("decode config: %w", err)
	}
	if err := applyDayDanCloudConfigAliases(v, &config); err != nil {
		return nil, err
	}

	configEnvironmentOverrides(&config)
	if value := envStripeWebhookSecret(); value != "" {
		config.StripeConfig.WebhookSecret = value
	}
	if err := resolveStripeWebhookSecret(&config); err != nil {
		return nil, err
	}
	if config.EmailConfig.AppHost == "" {
		config.EmailConfig.AppHost = v.GetString("email.appHost")
	}
	if err := initializePersistentRuntime(&config); err != nil {
		return nil, err
	}
	if err := validateSecurityConfig(&config); err != nil {
		return nil, err
	}

	config.Info.Version = Version
	config.Info.Commit = Commit
	config.Info.BuildDate = BuildDate

	// set the timezone to UTC if not set:
	if os.Getenv("TZ") == "" {
		os.Setenv("TZ", "UTC")
	}
	time.Local, _ = time.LoadLocation(os.Getenv("TZ"))

	return &config, nil
}

func validateSecurityConfig(cfg *Config) error {
	if cfg.Server.RatePeriod <= 0 || cfg.Server.RateLimit <= 0 {
		return fmt.Errorf("server rate period and limit must be positive")
	}
	if cfg.Server.RateLimitPasswordChangePeriod <= 0 || cfg.Server.RateLimitPasswordChange <= 0 {
		return fmt.Errorf("password-change rate period and limit must be positive")
	}
	if cfg.Server.RateLimitAuthPeriod <= 0 || cfg.Server.RateLimitAuth <= 0 {
		return fmt.Errorf("authentication rate period and limit must be positive")
	}
	if cfg.Server.RateLimitRecoveryPeriod <= 0 || cfg.Server.RateLimitRecovery <= 0 {
		return fmt.Errorf("recovery rate period and limit must be positive")
	}
	if cfg.Server.RateLimitMaxActiveBuckets <= 0 {
		return fmt.Errorf("authentication rate-limit max active buckets must be positive")
	}
	if cfg.WebhookConfig.Timeout <= 0 || cfg.WebhookConfig.QueueSize <= 0 {
		return fmt.Errorf("webhook timeout and queue_size must be positive")
	}
	if cfg.WebhookConfig.Workers <= 0 {
		return fmt.Errorf("webhook.workers must be positive")
	}
	if cfg.WebhookConfig.MaxAttempts <= 0 {
		return fmt.Errorf("webhook.max_attempts must be positive")
	}
	if cfg.WebhookConfig.BackoffInitial <= 0 || cfg.WebhookConfig.BackoffMax < cfg.WebhookConfig.BackoffInitial {
		return fmt.Errorf("webhook.backoff_initial and webhook.backoff_max must be positive with backoff_max >= backoff_initial")
	}
	if cfg.StripeConfig.WebhookTolerance < 0 || cfg.StripeConfig.WebhookTolerance > 24*time.Hour {
		return fmt.Errorf("stripe.webhook_tolerance must be between 0 and 24h")
	}
	mail := cfg.EmailConfig
	provider := strings.ToLower(strings.TrimSpace(mail.Provider))
	cfg.EmailConfig.Provider = provider
	configured := mail.Email != "" || mail.User != "" || mail.Key != "" || mail.Host != "" || mail.Port != 0 || mail.AppHost != "" || provider != "" || mail.ResetPasswordTemplateID != "" || mail.VerifyTemplateID != ""
	if mail.LogRawURL {
		return fmt.Errorf("email.log_raw_url is not supported; configure email delivery instead")
	}
	if configured {
		parsed, err := url.Parse(mail.AppHost)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") {
			return fmt.Errorf("email.app_host must be an absolute HTTP(S) URL")
		}
		switch provider {
		case "", "smtp":
			if mail.Email == "" || mail.Host == "" || mail.Port <= 0 {
				return fmt.Errorf("SMTP email delivery requires email, host, port, and app_host")
			}
		case "smtp2go":
			if mail.Email == "" || mail.Key == "" || mail.ResetPasswordTemplateID == "" || mail.VerifyTemplateID == "" {
				return fmt.Errorf("SMTP2Go delivery requires email, key, verify_template_id, reset_password_template_id, and app_host")
			}
		default:
			return fmt.Errorf("unsupported email provider %q", mail.Provider)
		}
	}
	for _, redirect := range cfg.OAuth2Config.AllowedRedirectURLs {
		parsed, err := url.Parse(redirect)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" || parsed.User != nil || parsed.Fragment != "" {
			return fmt.Errorf("oauth2.allowed_redirect_urls contains invalid URL %q", redirect)
		}
	}
	// Browser auth uses credential-bearing cookies, so CORS is always
	// credentials-enabled. A wildcard origin combined with Allow-Credentials
	// lets any website make authenticated requests against the instance;
	// reflect-everything CORS is not a supported configuration, fail fast at
	// startup instead of silently weakening browser isolation.
	for _, origin := range cfg.Server.CorsAllowOrigins {
		if strings.TrimSpace(origin) == "*" {
			return fmt.Errorf(`server.cors_allow_origins wildcard "*" is not allowed because credentials are always enabled; list explicit origins instead`)
		}
	}
	// Signed media URL TTL: must be positive and within the hard AWS SigV4
	// ceiling (7 days). Shorter values are supported — clients re-sign via
	// /api/v1/files/sign when a URL expires.
	if cfg.Storage.SignedURLTTL <= 0 {
		return fmt.Errorf("storage.signed_url_ttl must be positive")
	}
	if cfg.Storage.SignedURLTTL > MaxSignedURLTTL {
		return fmt.Errorf("storage.signed_url_ttl must not exceed %s (AWS SigV4 presigned URL maximum)", MaxSignedURLTTL)
	}
	// public_read intentionally serves objects anonymously, but only when an
	// operator has explicitly pointed the signer at a real bucket: enabling
	// it without a bucket name would silently mint unsigned URLs for objects
	// that may still be private. Empty/malformed values cannot enable it —
	// viper bool parsing rejects anything but true/1/TRUE-style input.
	if cfg.Storage.PublicRead {
		if strings.TrimSpace(cfg.Storage.BucketName) == "" {
			return fmt.Errorf("storage.public_read requires storage.bucket_name; the bucket/prefix must be configured for anonymous reads")
		}
	}
	return nil
}

// MaxSignedURLTTL is the hard ceiling for signed media URL lifetime. AWS
// SigV4 refuses to presign for longer than 7 days, so local HMAC URLs are
// held to the same bound to keep both credential families interchangeable
// from the client's perspective.
const MaxSignedURLTTL = 7 * 24 * time.Hour

func initializePersistentRuntime(config *Config) error {
	if config.DataDir == "" {
		config.DataDir = "data"
	}
	useLegacyWorkingDirectory := config.DataDir == "data"
	dataDir, err := filepath.Abs(config.DataDir)
	if err != nil {
		return fmt.Errorf("resolve data directory: %w", err)
	}
	config.DataDir = dataDir
	if err := os.MkdirAll(dataDir, 0o700); err != nil {
		return fmt.Errorf("create data directory %s: %w", dataDir, err)
	}
	if config.Database.Type == "" {
		config.Database.Type = "sqlite"
	}
	if config.Database.Type == "sqlite" {
		if path := envSQLitePath(); path != "" {
			config.Database.Path = path
		}
		if config.Database.Path == "" {
			path, err := resolveDefaultSQLitePath(dataDir, useLegacyWorkingDirectory)
			if err != nil {
				return fmt.Errorf("resolve SQLite path: %w", err)
			}
			config.Database.Path = path
		}
		if err := os.MkdirAll(filepath.Dir(config.Database.Path), 0o700); err != nil {
			return fmt.Errorf("create SQLite directory: %w", err)
		}
	}
	if config.Storage.StorageType == "local" && config.Storage.BasePath == "" {
		config.Storage.BasePath = filepath.Join(dataDir, "uploads")
	}
	if explicitSecret := envJWTSecret(); explicitSecret != "" {
		config.Jwt.Secret = explicitSecret
		if err := validateJWTSecret(config.Jwt.Secret); err != nil {
			return err
		}
		return resolveRuntimeSecrets(config)
	}
	if config.Jwt.Secret != "" && config.Jwt.Secret != "change_this_to_a_secure_random_string_32_characters_long" {
		if err := validateJWTSecret(config.Jwt.Secret); err != nil {
			return err
		}
		return resolveRuntimeSecrets(config)
	}
	config.Jwt.Secret = ""
	if config.Jwt.SecretFile == "" {
		config.Jwt.SecretFile = filepath.Join(dataDir, "jwt-secret")
	}
	secret, err := loadOrCreateSecret(config.Jwt.SecretFile)
	if err != nil {
		return fmt.Errorf("initialize persistent JWT secret: %w", err)
	}
	config.Jwt.Secret = secret
	if err := validateJWTSecret(secret); err != nil {
		return err
	}
	return resolveRuntimeSecrets(config)
}

// resolveRuntimeSecrets finalizes every derived/persistent credential family
// after the JWT secret is known.
func resolveRuntimeSecrets(cfg *Config) error {
	if err := resolveMediaSigningKey(cfg); err != nil {
		return err
	}
	return resolveWebhookSigningKey(cfg)
}

// mediaSigningKeyMinLen is the minimum length of an explicitly configured
// storage.media_secret. Derived keys are always 32 bytes of HKDF output and
// never go through this check.
const mediaSigningKeyMinLen = 16

// resolveMediaSigningKey finalizes the media URL signing key. Precedence:
//
//  1. an explicitly configured storage.media_secret (or its env override);
//  2. the contents of storage.media_secret_file (created with random content
//     on first boot, mirroring jwt.secret_file semantics);
//  3. HKDF-SHA256 expansion of jwt.secret under a storage-specific
//     info/salt label.
//
// Option 3 keeps zero-config self-hosted installs working after the phase-6
// key separation without any operator action. The derived key is
// computationally independent of the JWT key (HKDF is a PRF): knowing or
// rotating one reveals nothing about the other, but operators who want JWT
// rotation to leave media URLs untouched must configure option 1/2.
func resolveMediaSigningKey(cfg *Config) error {
	if value := envStorageMediaSecret(); value != "" {
		cfg.Storage.MediaSecret = value
	}
	secret := strings.TrimSpace(cfg.Storage.MediaSecret)
	if secret != "" {
		if len(secret) < mediaSigningKeyMinLen {
			return fmt.Errorf("storage.media_secret must be at least %d characters", mediaSigningKeyMinLen)
		}
		if secret == cfg.Jwt.Secret {
			// An operator who pastes the JWT secret as the media secret would
			// silently defeat key separation AND re-open the legacy scheme
			// forever, regardless of allow_legacy_media_signatures. Fail fast.
			return fmt.Errorf("storage.media_secret must differ from jwt.secret")
		}
		cfg.Storage.MediaSecret = secret
		return nil
	}
	cfg.Storage.MediaSecretFile = strings.TrimSpace(cfg.Storage.MediaSecretFile)
	if cfg.Storage.MediaSecretFile != "" {
		mediaKey, err := loadOrCreateSecret(cfg.Storage.MediaSecretFile)
		if err != nil {
			return fmt.Errorf("initialize persistent media signing secret: %w", err)
		}
		cfg.Storage.MediaSecret = mediaKey
		return nil
	}
	derived, err := deriveMediaSigningKey([]byte(cfg.Jwt.Secret))
	if err != nil {
		return fmt.Errorf("derive media signing secret: %w", err)
	}
	cfg.Storage.MediaSecret = hex.EncodeToString(derived)
	return nil
}

// mediaSigningKeyInfo and mediaSigningKeySalt are the HKDF domain-separation
// labels binding the derived media key to exactly this purpose. They must
// never be reused for another credential family (JWTs use raw jwt.secret).
const (
	mediaSigningKeyInfo = "daydan media-url-signing v1"
	mediaSigningKeySalt = "daydan/storage/media-url"
)

func deriveMediaSigningKey(jwtSecret []byte) ([]byte, error) {
	reader := hkdf.New(sha256.New, jwtSecret, []byte(mediaSigningKeySalt), []byte(mediaSigningKeyInfo))
	derived := make([]byte, 32)
	if _, err := io.ReadFull(reader, derived); err != nil {
		return nil, err
	}
	return derived, nil
}

// resolveWebhookSigningKey finalizes the outbound webhook HMAC key.
// Precedence:
//
//  1. an explicitly configured webhook.signing_secret;
//  2. the contents of webhook.signing_secret_file (created with random
//     content on first boot, mirroring jwt.secret_file semantics);
//  3. HKDF-SHA256 expansion of jwt.secret under a webhook-specific
//     info/salt label so zero-config installs sign out of the box while
//     the key remains computationally independent of every other family.
//
// Rotation: set signing_secret (or replace the secret file) and restart.
// Signatures produced by the old key stop verifying at receivers once they
// are told the new key; there is deliberately no silent legacy fallback.
func resolveWebhookSigningKey(cfg *Config) error {
	if value := envWebhookSigningSecret(); value != "" {
		cfg.WebhookConfig.SigningSecret = value
	}
	secret := strings.TrimSpace(cfg.WebhookConfig.SigningSecret)
	if secret != "" {
		if len(secret) < 16 {
			return fmt.Errorf("webhook.signing_secret must be at least 16 characters")
		}
		if secret == cfg.Jwt.Secret || secret == cfg.Storage.MediaSecret {
			return fmt.Errorf("webhook.signing_secret must differ from jwt.secret and storage.media_secret")
		}
		cfg.WebhookConfig.SigningSecret = secret
		return nil
	}
	cfg.WebhookConfig.SigningSecretFile = strings.TrimSpace(cfg.WebhookConfig.SigningSecretFile)
	if cfg.WebhookConfig.SigningSecretFile == "" {
		cfg.WebhookConfig.SigningSecretFile = filepath.Join(cfg.DataDir, "webhook-signing-secret")
	}
	key, err := loadOrCreateSecret(cfg.WebhookConfig.SigningSecretFile)
	if err != nil {
		return fmt.Errorf("initialize persistent webhook signing secret: %w", err)
	}
	cfg.WebhookConfig.SigningSecret = key
	return nil
}

// resolveStripeWebhookSecret loads stripe.webhook_secret from its optional
// file form. Called during startup so a misconfigured file fails fast.
func resolveStripeWebhookSecret(cfg *Config) error {
	if strings.TrimSpace(cfg.StripeConfig.WebhookSecret) == "" && strings.TrimSpace(cfg.StripeConfig.WebhookSecretFile) != "" {
		contents, err := os.ReadFile(strings.TrimSpace(cfg.StripeConfig.WebhookSecretFile))
		if err != nil {
			return fmt.Errorf("read stripe.webhook_secret_file: %w", err)
		}
		secret := strings.TrimSpace(string(contents))
		if secret == "" {
			return fmt.Errorf("stripe.webhook_secret_file %s is empty", cfg.StripeConfig.WebhookSecretFile)
		}
		cfg.StripeConfig.WebhookSecret = secret
	}
	return nil
}

func loadOrCreateSecret(path string) (string, error) {
	if contents, err := os.ReadFile(path); err == nil {
		secret := strings.TrimSpace(string(contents))
		if err := validateJWTSecret(secret); err != nil {
			return "", fmt.Errorf("invalid secret file %s: %w", path, err)
		}
		if err := os.Chmod(path, 0o600); err != nil {
			return "", err
		}
		return secret, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	secret, err := generateSecureSecret()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	temp, err := os.CreateTemp(filepath.Dir(path), ".jwt-secret-*")
	if err != nil {
		return "", err
	}
	tempName := temp.Name()
	defer os.Remove(tempName)
	if err := temp.Chmod(0o600); err != nil {
		temp.Close()
		return "", err
	}
	if _, err := temp.WriteString(secret + "\n"); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Sync(); err != nil {
		temp.Close()
		return "", err
	}
	if err := temp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tempName, path); err != nil {
		return "", err
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return "", err
	}
	return secret, nil
}

// List of known weak secrets that should not be allowed
var weakSecrets = []string{
	"secret",
	"mysecret",
	"jwt_secret",
	"default",
	"password",
	"123456",
	"changeme",
	"donetick",
	"jwt",
	"token",
	"key",
	"secretkey",
	"change_this_to_a_secure_random_string_32_characters_long",
}

// validateJWTSecret validates that the JWT secret is strong enough
func validateJWTSecret(secret string) error {
	var err error
	if len(secret) < 32 {
		err = fmt.Errorf("JWT secret must be at least 32 characters long, got %d characters", len(secret))
	}

	// Check against known weak secrets (case-insensitive)
	secretLower := strings.ToLower(secret)
	for _, weak := range weakSecrets {
		if secretLower == weak {
			err = fmt.Errorf("JWT secret is too weak: '%s' is not allowed", weak)
			break
		}
	}
	if err != nil {
		return err
	}
	return nil
}

// generateSecureSecret generates a cryptographically secure random secret
func generateSecureSecret() (string, error) {
	bytes := make([]byte, 32) // 256 bits
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(bytes), nil
}

// ParseLogLevel converts a string log level to zapcore.Level
func (c *LogConfig) ParseLogLevel() zapcore.Level {
	switch strings.ToLower(c.Level) {
	case "debug":
		return zapcore.DebugLevel
	case "info":
		return zapcore.InfoLevel
	case "warn", "warning":
		return zapcore.WarnLevel
	case "error":
		return zapcore.ErrorLevel
	case "dpanic":
		return zapcore.DPanicLevel
	case "panic":
		return zapcore.PanicLevel
	case "fatal":
		return zapcore.FatalLevel
	default:
		return zapcore.InfoLevel
	}
}
