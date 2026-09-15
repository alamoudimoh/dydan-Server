package authlimit

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
	uModel "github.com/alamoudimoh/dydan-server/internal/user/model"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var ErrCapacity = errors.New("authentication rate-limit capacity reached")

const maxKeyExtractionBody = 64 << 10

type Bucket struct {
	Key         string    `gorm:"column:key;type:char(64);primaryKey"`
	Scope       string    `gorm:"column:scope;type:varchar(64);not null"`
	WindowStart int64     `gorm:"column:window_start;not null"`
	Count       int64     `gorm:"column:count;not null"`
	ExpiresAt   time.Time `gorm:"column:expires_at;not null;index"`
}

func (Bucket) TableName() string { return "auth_rate_limit_buckets" }

type CapacityGuard struct {
	ID uint8 `gorm:"column:id;primaryKey"`
}

func (CapacityGuard) TableName() string { return "auth_rate_limit_capacity" }

type Limiter struct {
	db         *gorm.DB
	maxBuckets int64
	now        func() time.Time
}

type Result struct {
	Allowed   bool
	Limit     int64
	Remaining int64
	Reset     time.Time
}

type AccountField struct {
	Name            string
	CaseInsensitive bool
}

func New(db *gorm.DB, cfg *config.Config) *Limiter {
	maxBuckets := cfg.Server.RateLimitMaxActiveBuckets
	if maxBuckets <= 0 {
		maxBuckets = 100000
	}
	return &Limiter{db: db, maxBuckets: int64(maxBuckets), now: func() time.Time { return time.Now().UTC() }}
}

func (l *Limiter) Allow(ctx context.Context, scope, source string, period time.Duration, limit int64) (Result, error) {
	if period <= 0 || limit <= 0 {
		return Result{}, errors.New("invalid authentication rate limit")
	}
	now := l.now().UTC()
	windowStart := now.UnixNano() / int64(period) * int64(period)
	reset := time.Unix(0, windowStart+int64(period)).UTC()
	key := hashKey(scope, source)

	var existing int64
	if err := l.db.WithContext(ctx).Model(&Bucket{}).Where("key = ?", key).Count(&existing).Error; err != nil {
		return Result{}, err
	}
	bucket := Bucket{Key: key, Scope: bounded(scope, 64), WindowStart: windowStart, Count: 1, ExpiresAt: reset}
	if existing == 0 {
		err := l.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
			if err := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&CapacityGuard{ID: 1}).Error; err != nil {
				return err
			}
			var guard CapacityGuard
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&guard, 1).Error; err != nil {
				return err
			}
			var admitted int64
			if err := tx.Model(&Bucket{}).Where("key = ?", key).Count(&admitted).Error; err != nil {
				return err
			}
			if admitted == 0 {
				if err := tx.Where("expires_at <= ?", now).Delete(&Bucket{}).Error; err != nil {
					return err
				}
				var active int64
				if err := tx.Model(&Bucket{}).Where("expires_at > ?", now).Count(&active).Error; err != nil {
					return err
				}
				if active >= l.maxBuckets {
					return ErrCapacity
				}
			}
			return increment(tx, &bucket, windowStart, reset)
		})
		if err != nil {
			return Result{}, err
		}
	} else if err := increment(l.db.WithContext(ctx), &bucket, windowStart, reset); err != nil {
		return Result{}, err
	}

	remaining := limit - bucket.Count
	if remaining < 0 {
		remaining = 0
	}
	return Result{Allowed: bucket.Count <= limit, Limit: limit, Remaining: remaining, Reset: reset}, nil
}

func increment(db *gorm.DB, bucket *Bucket, windowStart int64, reset time.Time) error {
	table := Bucket{}.TableName()
	windowColumn := table + ".window_start"
	countColumn := table + ".count"
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "key"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"scope":        bucket.Scope,
			"window_start": gorm.Expr("CASE WHEN "+windowColumn+" = ? THEN "+windowColumn+" ELSE ? END", windowStart, windowStart),
			"count":        gorm.Expr("CASE WHEN "+windowColumn+" = ? THEN "+countColumn+" + 1 ELSE 1 END", windowStart),
			"expires_at":   reset,
		}),
	}, clause.Returning{Columns: []clause.Column{{Name: "count"}, {Name: "window_start"}, {Name: "expires_at"}}}).Create(bucket).Error
}

func (l *Limiter) Cleanup(ctx context.Context, now time.Time) error {
	return l.db.WithContext(ctx).Where("expires_at <= ?", now.UTC()).Delete(&Bucket{}).Error
}

func (l *Limiter) Middleware(scope string, period time.Duration, limit int64, fields ...AccountField) gin.HandlerFunc {
	return func(c *gin.Context) {
		sources := []string{"ip:" + CanonicalIP(c.ClientIP())}
		if current, ok := c.Get("id"); ok {
			if user, ok := current.(*uModel.UserDetails); ok {
				sources = append(sources, "user:"+strconv.Itoa(user.ID))
			}
		}
		if len(fields) > 0 {
			bodyKeys, err := bodySources(c, fields)
			if err != nil {
				c.AbortWithStatusJSON(http.StatusRequestEntityTooLarge, gin.H{"error": "Authentication request is too large", "code": "AUTH_REQUEST_TOO_LARGE"})
				return
			}
			sources = append(sources, bodyKeys...)
		}
		var mostRestrictive Result
		mostRestrictive.Remaining = limit
		for _, source := range sources {
			result, err := l.Allow(c.Request.Context(), scope, source, period, limit)
			if err != nil {
				c.AbortWithStatusJSON(http.StatusServiceUnavailable, gin.H{"error": "Authentication service temporarily unavailable", "code": "AUTH_RATE_LIMIT_UNAVAILABLE"})
				return
			}
			if result.Remaining < mostRestrictive.Remaining {
				mostRestrictive = result
			}
			if !result.Allowed {
				setHeaders(c, result)
				retry := time.Until(result.Reset)
				if retry < time.Second {
					retry = time.Second
				}
				c.Header("Retry-After", strconv.FormatInt(int64((retry+time.Second-1)/time.Second), 10))
				c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{"error": "Too many authentication attempts", "code": "AUTH_RATE_LIMITED"})
				return
			}
		}
		setHeaders(c, mostRestrictive)
		c.Next()
	}
}

func CanonicalIP(value string) string {
	ip := net.ParseIP(strings.TrimSpace(value))
	if ip == nil {
		return "invalid"
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

func bodySources(c *gin.Context, fields []AccountField) ([]string, error) {
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, maxKeyExtractionBody+1))
	if err != nil {
		return nil, err
	}
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if len(body) > maxKeyExtractionBody {
		return nil, errors.New("authentication request body exceeds key extraction limit")
	}
	values := make(map[string]interface{})
	_ = json.Unmarshal(body, &values)
	result := make([]string, 0, len(fields))
	for _, field := range fields {
		value, ok := values[field.Name].(string)
		if !ok && field.Name == "refresh_token" {
			value, _ = c.Cookie(field.Name)
		}
		value = strings.TrimSpace(value)
		if field.CaseInsensitive {
			value = strings.ToLower(value)
		}
		if value != "" {
			result = append(result, "account:"+field.Name+":"+digest(value))
		}
	}
	return result, nil
}

func hashKey(scope, source string) string {
	sum := sha256.Sum256([]byte(bounded(scope, 64) + "\x00" + source))
	return hex.EncodeToString(sum[:])
}

func digest(value string) string {
	sum := sha256.Sum256([]byte(value))
	return hex.EncodeToString(sum[:])
}

func bounded(value string, max int) string {
	if len(value) > max {
		return value[:max]
	}
	return value
}

func setHeaders(c *gin.Context, result Result) {
	c.Header("X-RateLimit-Limit", strconv.FormatInt(result.Limit, 10))
	c.Header("X-RateLimit-Remaining", strconv.FormatInt(result.Remaining, 10))
	c.Header("X-RateLimit-Reset", strconv.FormatInt(result.Reset.Unix(), 10))
}
