package config

import (
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"go.uber.org/zap/zapcore"

	"github.com/spf13/viper"
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
}

type JwtConfig struct {
	Secret      string        `mapstructure:"secret" yaml:"secret"`
	SecretFile  string        `mapstructure:"secret_file" yaml:"secret_file"`
	SessionTime time.Duration `mapstructure:"session_time" yaml:"session_time" default:"24h"` // 24 hours
	MaxRefresh  time.Duration `mapstructure:"max_refresh" yaml:"max_refresh" default:"1440h"` // 60 days
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
	AppHost   string `mapstructure:"appHost"`
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
	ClientID      string   `mapstructure:"client_id" yaml:"client_id"`
	ClientSecret  string   `mapstructure:"client_secret" yaml:"client_secret"`
	RedirectURL   string   `mapstructure:"redirect_url" yaml:"redirect_url"`
	Scopes        []string `mapstructure:"scopes" yaml:"scopes"`
	AuthURL       string   `mapstructure:"auth_url" yaml:"auth_url"`
	TokenURL      string   `mapstructure:"token_url" yaml:"token_url"`
	UserInfoURL   string   `mapstructure:"user_info_url" yaml:"user_info_url"`
	Name          string   `mapstructure:"name" yaml:"name"`
	AdminGroups   []string `mapstructure:"admin_groups" yaml:"admin_groups"`
	ManagerGroups []string `mapstructure:"manager_groups" yaml:"manager_groups"`
}

type WebhookConfig struct {
	Timeout   time.Duration `mapstructure:"timeout" yaml:"timeout" default:"5s"`
	QueueSize int           `mapstructure:"queue_size" yaml:"queue_size" default:"100"`
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
			SessionTime: 24 * time.Hour,
			MaxRefresh:  60 * 24 * time.Hour,
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
		WebhookConfig: WebhookConfig{Timeout: 5 * time.Second, QueueSize: 100},
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
			StorageType:    "local",
			MaxUserStorage: 10 * 1024 * 1024 * 1024,
			MaxFileSize:    10 * 1024 * 1024,
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
}
func setDayDanDefaults(v *viper.Viper) {
	cfg := NewConfig()
	v.SetDefault("name", cfg.Name)
	v.SetDefault("data_dir", cfg.DataDir)
	v.SetDefault("is_done_tick_dot_com", false)
	v.SetDefault("is_user_creation_disabled", false)
	v.SetDefault("database.type", cfg.Database.Type)
	v.SetDefault("database.migration", cfg.Database.Migration)
	v.SetDefault("jwt.session_time", cfg.Jwt.SessionTime)
	v.SetDefault("jwt.max_refresh", cfg.Jwt.MaxRefresh)
	v.SetDefault("server.port", cfg.Server.Port)
	v.SetDefault("server.rate_period", cfg.Server.RatePeriod)
	v.SetDefault("server.rate_limit", cfg.Server.RateLimit)
	v.SetDefault("server.read_timeout", cfg.Server.ReadTimeout)
	v.SetDefault("server.write_timeout", cfg.Server.WriteTimeout)
	v.SetDefault("server.webhook_timeout", cfg.Server.WebhookTimeout)
	v.SetDefault("server.cors_allow_origins", cfg.Server.CorsAllowOrigins)
	v.SetDefault("server.serve_frontend", cfg.Server.ServeFrontend)
	v.SetDefault("server.serve_swagger", false)
	v.SetDefault("scheduler_jobs.due_job", cfg.SchedulerJobs.DueJob)
	v.SetDefault("scheduler_jobs.overdue_job", cfg.SchedulerJobs.OverdueJob)
	v.SetDefault("scheduler_jobs.pre_due_job", cfg.SchedulerJobs.PreDueJob)
	v.SetDefault("storage.storage_type", cfg.Storage.StorageType)
	v.SetDefault("storage.max_user_storage", cfg.Storage.MaxUserStorage)
	v.SetDefault("storage.max_file_size", cfg.Storage.MaxFileSize)
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
	v.SetConfigType("yaml")

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
	if err := initializePersistentRuntime(&config); err != nil {
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
		return validateJWTSecret(config.Jwt.Secret)
	}
	if config.Jwt.Secret != "" && config.Jwt.Secret != "change_this_to_a_secure_random_string_32_characters_long" {
		return validateJWTSecret(config.Jwt.Secret)
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
	return validateJWTSecret(secret)
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
