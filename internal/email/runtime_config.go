package email

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	"gorm.io/gorm"
)

// RuntimeConfiguration is the singleton, encrypted-at-rest administrator
// delivery setting. SecretCiphertext is deliberately never serialized.
type RuntimeConfiguration struct {
	ID                      int       `gorm:"primaryKey" json:"-"`
	Provider                string    `gorm:"not null" json:"-"`
	FromEmail               string    `gorm:"column:from_email;not null" json:"-"`
	Host                    string    `json:"-"`
	Port                    int       `json:"-"`
	Username                string    `json:"-"`
	AppHost                 string    `gorm:"column:app_host;not null" json:"-"`
	VerifyTemplateID        string    `gorm:"column:verify_template_id" json:"-"`
	ResetPasswordTemplateID string    `gorm:"column:reset_password_template_id" json:"-"`
	SecretCiphertext        string    `gorm:"column:secret_ciphertext;type:text;not null" json:"-"`
	CreatedAt               time.Time `json:"-"`
	UpdatedAt               time.Time `json:"-"`
}

func (RuntimeConfiguration) TableName() string { return "email_runtime_configurations" }

type DeliveryStatus struct {
	Configured              bool   `json:"configured"`
	Source                  string `json:"source"`
	Provider                string `json:"provider,omitempty"`
	FromEmail               string `json:"fromEmail,omitempty"`
	Host                    string `json:"host,omitempty"`
	Port                    int    `json:"port,omitempty"`
	Username                string `json:"username,omitempty"`
	AppHost                 string `json:"appHost,omitempty"`
	VerifyTemplateID        string `json:"verificationTemplateId,omitempty"`
	ResetPasswordTemplateID string `json:"resetPasswordTemplateId,omitempty"`
	SecretConfigured        bool   `json:"secretConfigured"`
}

type DeliveryInput struct {
	Provider                string `json:"provider"`
	FromEmail               string `json:"fromEmail"`
	Host                    string `json:"host"`
	Port                    int    `json:"port"`
	Username                string `json:"username"`
	Secret                  string `json:"secret"`
	AppHost                 string `json:"appHost"`
	VerifyTemplateID        string `json:"verificationTemplateId"`
	ResetPasswordTemplateID string `json:"resetPasswordTemplateId"`
}

type RuntimeStore struct {
	cfg *config.Config
	db  *gorm.DB
}

func NewRuntimeStore(cfg *config.Config, db *gorm.DB) *RuntimeStore {
	return &RuntimeStore{cfg: cfg, db: db}
}

func (s *RuntimeStore) configuredFallback() bool {
	c := s.cfg.EmailConfig
	return strings.TrimSpace(c.Email) != "" && strings.TrimSpace(c.AppHost) != "" &&
		((strings.EqualFold(strings.TrimSpace(c.Provider), "smtp2go") && strings.TrimSpace(c.Key) != "" && strings.TrimSpace(c.VerifyTemplateID) != "" && strings.TrimSpace(c.ResetPasswordTemplateID) != "") ||
			(!strings.EqualFold(strings.TrimSpace(c.Provider), "smtp2go") && strings.TrimSpace(c.Host) != "" && c.Port > 0))
}

func normalProvider(provider string) string {
	if strings.EqualFold(strings.TrimSpace(provider), "smtp2go") {
		return "smtp2go"
	}
	return "smtp"
}

func validateInput(in DeliveryInput, hasSecret bool) (DeliveryInput, error) {
	in.Provider = normalProvider(in.Provider)
	in.FromEmail = strings.TrimSpace(in.FromEmail)
	in.Host = strings.TrimSpace(in.Host)
	in.Username = strings.TrimSpace(in.Username)
	in.AppHost = strings.TrimRight(strings.TrimSpace(in.AppHost), "/")
	in.VerifyTemplateID = strings.TrimSpace(in.VerifyTemplateID)
	in.ResetPasswordTemplateID = strings.TrimSpace(in.ResetPasswordTemplateID)
	if _, err := mail.ParseAddress(in.FromEmail); err != nil {
		return in, errors.New("a valid from email is required")
	}
	parsed, err := url.Parse(in.AppHost)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return in, errors.New("app host must be an absolute HTTP(S) URL")
	}
	if !hasSecret {
		return in, errors.New("a password or API key is required")
	}
	if in.Provider == "smtp" {
		if in.Host == "" || in.Port < 1 || in.Port > 65535 {
			return in, errors.New("SMTP host and a valid port are required")
		}
		return in, nil
	}
	if in.VerifyTemplateID == "" || in.ResetPasswordTemplateID == "" {
		return in, errors.New("SMTP2GO verification and reset-password template IDs are required")
	}
	return in, nil
}

func (s *RuntimeStore) encryptionKey() []byte {
	key := sha256.Sum256([]byte(s.cfg.EmailConfig.RuntimeSecret))
	return key[:]
}

func (s *RuntimeStore) encrypt(secret string) (string, error) {
	block, err := aes.NewCipher(s.encryptionKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	return base64.RawStdEncoding.EncodeToString(append(nonce, gcm.Seal(nil, nonce, []byte(secret), nil)...)), nil
}

func (s *RuntimeStore) decrypt(ciphertext string) (string, error) {
	data, err := base64.RawStdEncoding.DecodeString(ciphertext)
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(s.encryptionKey())
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil || len(data) < gcm.NonceSize() {
		return "", errors.New("invalid encrypted email configuration")
	}
	plain, err := gcm.Open(nil, data[:gcm.NonceSize()], data[gcm.NonceSize():], nil)
	if err != nil {
		return "", errors.New("cannot decrypt saved email configuration")
	}
	return string(plain), nil
}

func statusFromRecord(record RuntimeConfiguration) DeliveryStatus {
	return DeliveryStatus{Configured: true, Source: "runtime", Provider: record.Provider, FromEmail: record.FromEmail, Host: record.Host, Port: record.Port, Username: record.Username, AppHost: record.AppHost, VerifyTemplateID: record.VerifyTemplateID, ResetPasswordTemplateID: record.ResetPasswordTemplateID, SecretConfigured: record.SecretCiphertext != ""}
}

func (s *RuntimeStore) Status(ctx context.Context) (DeliveryStatus, error) {
	var record RuntimeConfiguration
	err := s.db.WithContext(ctx).First(&record, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if !s.configuredFallback() {
			return DeliveryStatus{Configured: false, Source: "none", Provider: "smtp"}, nil
		}
		fallback := s.cfg.EmailConfig
		return DeliveryStatus{Configured: true, Source: "environment", Provider: normalProvider(fallback.Provider), FromEmail: fallback.Email, Host: fallback.Host, Port: fallback.Port, Username: fallback.User, AppHost: fallback.AppHost, VerifyTemplateID: fallback.VerifyTemplateID, ResetPasswordTemplateID: fallback.ResetPasswordTemplateID, SecretConfigured: fallback.Key != ""}, nil
	}
	if err != nil {
		return DeliveryStatus{}, err
	}
	return statusFromRecord(record), nil
}

func (s *RuntimeStore) Save(ctx context.Context, in DeliveryInput) (DeliveryStatus, error) {
	var existing RuntimeConfiguration
	err := s.db.WithContext(ctx).First(&existing, 1).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return DeliveryStatus{}, err
	}
	hasExistingSecret := err == nil && existing.SecretCiphertext != ""
	in, err = validateInput(in, strings.TrimSpace(in.Secret) != "" || hasExistingSecret)
	if err != nil {
		return DeliveryStatus{}, err
	}
	record := RuntimeConfiguration{ID: 1, Provider: in.Provider, FromEmail: in.FromEmail, Host: in.Host, Port: in.Port, Username: in.Username, AppHost: in.AppHost, VerifyTemplateID: in.VerifyTemplateID, ResetPasswordTemplateID: in.ResetPasswordTemplateID}
	if strings.TrimSpace(in.Secret) == "" {
		record.SecretCiphertext = existing.SecretCiphertext
	} else {
		ciphertext, err := s.encrypt(in.Secret)
		if err != nil {
			return DeliveryStatus{}, fmt.Errorf("encrypt email configuration: %w", err)
		}
		record.SecretCiphertext = ciphertext
	}
	if err := s.db.WithContext(ctx).Save(&record).Error; err != nil {
		return DeliveryStatus{}, err
	}
	return statusFromRecord(record), nil
}

func (s *RuntimeStore) currentConfig(ctx context.Context) (*config.Config, error) {
	var record RuntimeConfiguration
	err := s.db.WithContext(ctx).First(&record, 1).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		if !s.configuredFallback() {
			return nil, ErrDeliveryDisabled
		}
		copy := *s.cfg
		return &copy, nil
	}
	if err != nil {
		return nil, err
	}
	secret, err := s.decrypt(record.SecretCiphertext)
	if err != nil {
		return nil, err
	}
	copy := *s.cfg
	copy.EmailConfig = config.EmailConfig{Provider: record.Provider, Email: record.FromEmail, User: record.Username, Key: secret, Host: record.Host, Port: record.Port, AppHost: record.AppHost, VerifyTemplateID: record.VerifyTemplateID, ResetPasswordTemplateID: record.ResetPasswordTemplateID}
	return &copy, nil
}

type RuntimeSender struct{ store *RuntimeStore }

func (s *RuntimeSender) sender(ctx context.Context) (Sender, error) {
	cfg, err := s.store.currentConfig(ctx)
	if err != nil {
		return nil, err
	}
	if normalProvider(cfg.EmailConfig.Provider) == "smtp2go" {
		return NewSMTP2GoSender(cfg), nil
	}
	return NewEmailSender(cfg), nil
}

func (s *RuntimeSender) SendVerificationEmail(to, code string) error {
	sender, err := s.sender(context.Background())
	if err != nil {
		return err
	}
	return sender.SendVerificationEmail(to, code)
}

func (s *RuntimeSender) SendResetPasswordEmail(ctx context.Context, to, code string) error {
	sender, err := s.sender(ctx)
	if err != nil {
		return err
	}
	return sender.SendResetPasswordEmail(ctx, to, code)
}

func (s *RuntimeSender) SendTestEmail(ctx context.Context, to string) error {
	sender, err := s.sender(ctx)
	if err != nil {
		return err
	}
	return sender.SendTestEmail(ctx, to)
}
