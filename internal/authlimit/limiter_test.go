package authlimit

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func sharedLimiters(t *testing.T, max int) (*Limiter, *Limiter, *gorm.DB) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "limits.db")
	dsn := "file:" + filepath.ToSlash(path) + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(10000)"
	open := func() *gorm.DB {
		db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{})
		if err != nil {
			t.Fatal(err)
		}
		return db
	}
	dbA, dbB := open(), open()
	if err := dbA.AutoMigrate(&Bucket{}, &CapacityGuard{}); err != nil {
		t.Fatal(err)
	}
	cfg := &config.Config{}
	cfg.Server.RateLimitMaxActiveBuckets = max
	return New(dbA, cfg), New(dbB, cfg), dbA
}

func TestSharedAttemptsAcrossHandles(t *testing.T) {
	a, b, _ := sharedLimiters(t, 100)
	for i, limiter := range []*Limiter{a, b, a} {
		result, err := limiter.Allow(context.Background(), "login", "ip:192.0.2.1", time.Minute, 2)
		if err != nil {
			t.Fatal(err)
		}
		if result.Allowed != (i < 2) {
			t.Fatalf("attempt %d allowed=%v", i+1, result.Allowed)
		}
	}
}

func TestCanonicalIPAndHashedBoundedKeys(t *testing.T) {
	if CanonicalIP("2001:db8:abcd:12::1") != CanonicalIP("2001:db8:abcd:12:ffff::2") {
		t.Fatal("IPv6 addresses in one /64 must share a source")
	}
	if got := CanonicalIP("192.0.2.001"); got != "invalid" {
		t.Fatalf("non-canonical IP should be invalid, got %q", got)
	}
	a, _, db := sharedLimiters(t, 100)
	long := "account:email:" + fmt.Sprintf("%0600d", 1)
	if _, err := a.Allow(context.Background(), "recovery", long, time.Minute, 1); err != nil {
		t.Fatal(err)
	}
	var bucket Bucket
	if err := db.First(&bucket).Error; err != nil {
		t.Fatal(err)
	}
	if len(bucket.Key) != 64 || bucket.Key == long {
		t.Fatalf("persisted key is not a fixed SHA-256 digest: %q", bucket.Key)
	}
}

func TestAccountNormalizationSharesBucket(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a, _, db := sharedLimiters(t, 100)
	r := gin.New()
	r.POST("/", a.Middleware("login", time.Minute, 1, AccountField{Name: "username", CaseInsensitive: true}), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	request := func(body, remote string) int {
		req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
		req.Header.Set("Content-Type", "application/json")
		req.RemoteAddr = remote
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}
	if got := request(`{"username":"  Alice@Example.COM "}`, "192.0.2.10:1234"); got != http.StatusNoContent {
		t.Fatalf("first request status=%d", got)
	}
	if got := request(`{"username":"alice@example.com"}`, "192.0.2.11:1234"); got != http.StatusTooManyRequests {
		t.Fatalf("normalized account did not share bucket, status=%d", got)
	}
	var count int64
	if err := db.Model(&Bucket{}).Count(&count).Error; err != nil || count != 3 {
		// Two IP buckets and one normalized account bucket; no identifier is stored.
		t.Fatalf("bucket count=%d err=%v", count, err)
	}
}

func TestMiddlewareFailsClosedOnDatastoreError(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a, _, db := sharedLimiters(t, 100)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if err := sqlDB.Close(); err != nil {
		t.Fatal(err)
	}
	r := gin.New()
	r.POST("/", a.Middleware("login", time.Minute, 1), func(c *gin.Context) { c.Status(http.StatusNoContent) })
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", nil))
	if w.Code != http.StatusServiceUnavailable || !bytes.Contains(w.Body.Bytes(), []byte("AUTH_RATE_LIMIT_UNAVAILABLE")) {
		t.Fatalf("datastore failure response=%d %s", w.Code, w.Body.String())
	}
}

func TestMiddlewareRejectsOversizedAuthenticationBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	a, _, _ := sharedLimiters(t, 100)
	r := gin.New()
	r.POST("/", a.Middleware("login", time.Minute, 1, AccountField{Name: "username"}), func(c *gin.Context) {
		c.Status(http.StatusNoContent)
	})
	body := bytes.Repeat([]byte("a"), maxKeyExtractionBody+1)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/", bytes.NewReader(body)))
	if w.Code != http.StatusRequestEntityTooLarge || !bytes.Contains(w.Body.Bytes(), []byte("AUTH_REQUEST_TOO_LARGE")) {
		t.Fatalf("oversized body response=%d %s", w.Code, w.Body.String())
	}
}

func TestExpiryCleanupAndCapacityFailClosed(t *testing.T) {
	a, _, db := sharedLimiters(t, 1)
	now := time.Date(2026, 8, 24, 12, 0, 0, 0, time.UTC)
	a.now = func() time.Time { return now }
	if _, err := a.Allow(context.Background(), "login", "ip:one", time.Minute, 2); err != nil {
		t.Fatal(err)
	}
	if _, err := a.Allow(context.Background(), "login", "ip:two", time.Minute, 2); err != ErrCapacity {
		t.Fatalf("capacity error = %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, err := a.Allow(context.Background(), "login", "ip:two", time.Minute, 2); err != nil {
		t.Fatalf("expired bucket was not reclaimed: %v", err)
	}
	var count int64
	if err := db.Model(&Bucket{}).Count(&count).Error; err != nil || count != 1 {
		t.Fatalf("bucket count=%d err=%v", count, err)
	}
}

func TestConcurrentExactWinnersAcrossHandles(t *testing.T) {
	a, b, _ := sharedLimiters(t, 100)
	var winners atomic.Int64
	var failures atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(1)
		limiter := a
		if i%2 == 1 {
			limiter = b
		}
		go func() {
			defer wg.Done()
			result, err := limiter.Allow(context.Background(), "mfa", "account:session:abc", time.Minute, 7)
			if err != nil {
				failures.Add(1)
				return
			}
			if result.Allowed {
				winners.Add(1)
			}
		}()
	}
	wg.Wait()
	if failures.Load() != 0 || winners.Load() != 7 {
		t.Fatalf("failures=%d winners=%d, want 0 and 7", failures.Load(), winners.Load())
	}
}

func TestConcurrentNewKeysRespectCapacity(t *testing.T) {
	a, b, db := sharedLimiters(t, 3)
	var admitted atomic.Int64
	var unexpected atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		limiter := a
		if i%2 == 1 {
			limiter = b
		}
		go func(key int) {
			defer wg.Done()
			_, err := limiter.Allow(context.Background(), "login", fmt.Sprintf("ip:%d", key), time.Minute, 1)
			switch err {
			case nil:
				admitted.Add(1)
			case ErrCapacity:
			default:
				unexpected.Add(1)
			}
		}(i)
	}
	wg.Wait()
	var count int64
	if err := db.Model(&Bucket{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if admitted.Load() != 3 || unexpected.Load() != 0 || count != 3 {
		t.Fatalf("admitted=%d unexpected=%d rows=%d, want 3, 0, 3", admitted.Load(), unexpected.Load(), count)
	}
}

// TestForgedForwardedHeadersCannotBypassLimits pins that abuse-control keying
// uses the effective ClientIP (which ignores X-Forwarded-For from untrusted
// direct peers): a direct client cannot rotate its limit bucket by rotating
// forged forwarding headers.
func TestForgedForwardedHeadersCannotBypassLimits(t *testing.T) {
	limiter, _, db := sharedLimiters(t, 100)
	gin.SetMode(gin.TestMode)
	r := gin.New()
	_ = r.SetTrustedProxies([]string{"10.0.0.1/32"}) // production-style allowlist
	r.POST("/login", limiter.Middleware("login", time.Minute, 2), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})

	do := func(xff string) int {
		req := httptest.NewRequest(http.MethodPost, "/login", bytes.NewReader([]byte(`{"username":"u"}`)))
		req.RemoteAddr = "203.0.113.5:1234"
		if xff != "" {
			req.Header.Set("X-Forwarded-For", xff)
		}
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code
	}

	if code := do(""); code != http.StatusOK {
		t.Fatalf("first attempt: %d", code)
	}
	if code := do("8.8.8.8"); code != http.StatusOK {
		t.Fatalf("forged header must not open a fresh bucket, got %d", code)
	}
	// Bucket now holds 2 of 2 allowed attempts: the next try is limited even
	// with yet another freshly forged identity.
	if code := do("7.7.7.7"); code != http.StatusTooManyRequests {
		t.Fatalf("attempt must be limited regardless of forged headers, got %d", code)
	}

	var count int64
	db.Model(&Bucket{}).Where("scope = ?", "login").Count(&count)
	if count != 1 {
		t.Fatalf("all attempts must land in exactly one bucket, got %d", count)
	}
}

// TestLimitResponseDoesNotRevealAccountExistence pins the enumeration
// contract: a rate-limited response is byte-identical whether or not the
// targeted account exists.
func TestLimitResponseDoesNotRevealAccountExistence(t *testing.T) {
	limiter, _, _ := sharedLimiters(t, 100)
	gin.SetMode(gin.TestMode)

	run := func(username string) (int, string, string) {
		r := gin.New()
		r.POST("/login", limiter.Middleware("login", time.Minute, 0, AccountField{Name: "username"}), func(c *gin.Context) { c.Status(200) })
		req := httptest.NewRequest(http.MethodPost, "/login",
			bytes.NewReader([]byte(`{"username":"`+username+`"}`)))
		req.RemoteAddr = "198.51.100.9:1"
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w.Code, w.Body.String(), w.Header().Get("Retry-After")
	}

	codeA, bodyA, retryA := run("existing-user")
	codeB, bodyB, retryB := run("nonexistent-user")
	if codeA != codeB || bodyA != bodyB || retryA != retryB {
		t.Fatalf("limit responses differ by account existence: (%d,%q,%q) vs (%d,%q,%q)",
			codeA, bodyA, retryA, codeB, bodyB, retryB)
	}
}
