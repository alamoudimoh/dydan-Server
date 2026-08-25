package events

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
	chModel "github.com/alamoudimoh/daydan-server/internal/chore/model"
	uModel "github.com/alamoudimoh/daydan-server/internal/user/model"
	"github.com/alamoudimoh/daydan-server/logging"
)

// startTestServer runs an httptest server whose handler records hits and can
// block until released. Returns the URL and a cleanup func.
func startTestServer(t *testing.T, onBody func([]byte, map[string]string)) (string, func()) {
	t.Helper()
	block := make(chan struct{})
	var closeOnce sync.Once
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		hdr := map[string]string{}
		for k := range r.Header {
			hdr[k] = r.Header.Get(k)
		}
		if onBody != nil {
			onBody(raw, hdr)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	return server.URL, func() {
		closeOnce.Do(func() { close(block) })
		server.Close()
	}
}

func newTestProducer(t *testing.T) *EventsProducer {
	t.Helper()
	cfg := config.NewConfig()
	cfg.WebhookConfig.Timeout = time.Second
	cfg.WebhookConfig.QueueSize = 100
	cfg.WebhookConfig.Workers = 8
	cfg.WebhookConfig.MaxAttempts = 4
	cfg.WebhookConfig.BackoffInitial = time.Millisecond
	cfg.WebhookConfig.BackoffMax = 10 * time.Millisecond
	cfg.WebhookConfig.AllowPrivateNetworks = true
	cfg.WebhookConfig.SigningSecret = testSigningSecret
	p := NewEventsProducer(cfg)
	p.logger = logging.FromContext(context.Background())
	return p
}

func parseInt64(t *testing.T, s string) int64 {
	t.Helper()
	v, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("bad integer header %q: %v", s, err)
	}
	return v
}

// waitFor polls cond until it holds or the deadline expires.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestTenantIsolationSlowTenantCannotStarveOthers reproduces the Phase-7
// residual: before tenant isolation a single slow destination blocked the
// lone worker and delayed every other tenant indefinitely.
func TestTenantIsolationSlowTenantCannotStarveOthers(t *testing.T) {
	var bHits atomic.Int32
	stall := make(chan struct{})
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-stall // never respond while the test runs
		w.WriteHeader(http.StatusNoContent)
	}))
	// Unblock the handler before Close (cleanups run after defers).
	defer func() { close(stall); srvA.Close() }()
	srvB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		bHits.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srvB.Close()

	p := newTestProducer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	// Tenant A's delivery occupies its slot and hangs.
	p.publishEvent(Event{Type: EventTypeThingChanged, URL: srvA.URL, Data: map[string]any{"t": "a"}})
	// Wait until A's delivery is claimed (bucket busy), then flood B.
	waitFor(t, "tenant A delivery claimed", func() bool {
		p.mu.Lock()
		defer p.mu.Unlock()
		b, ok := p.byKey[srvA.URL]
		return ok && b.busy
	})

	for i := 0; i < 20; i++ {
		p.publishEvent(Event{Type: EventTypeThingChanged, URL: srvB.URL, Data: map[string]any{"n": i}})
	}
	waitFor(t, "tenant B deliveries while A stalled", func() bool { return bHits.Load() >= 10 })
	cancel()
}

// TestFloodedTenantDropsWithoutBlockingOthers proves per-tenant bounds:
// flooding one queue must not deadlock publishers or starve other tenants.
func TestFloodedTenantDropsWithoutBlockingOthers(t *testing.T) {
	var aHits, bHits atomic.Int32
	srvA := httptest.NewServer(countHandler(&aHits))
	defer srvA.Close()
	srvB := httptest.NewServer(countHandler(&bHits))
	defer srvB.Close()

	p := newTestProducer(t)
	p.perTenantCap_ = 5
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 200; i++ {
			p.publishEvent(Event{Type: EventTypeTaskReminder, URL: srvA.URL, Data: map[string]any{"i": i}})
		}
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("publishing to a full tenant queue blocked")
	}

	// The global queue may legitimately drop a single publish while the
	// flood occupies it; keep offering B's event until it lands.
	waitFor(t, "other-tenant delivery during flood", func() bool {
		if bHits.Load() == 0 {
			p.publishEvent(Event{Type: EventTypeTaskReminder, URL: srvB.URL, Data: map[string]any{"x": 1}})
		}
		return bHits.Load() >= 1
	})
	cancel()
}

// TestQueueCapacityBackpressureIsNonBlocking proves the global queue bound
// drops instead of blocking when capacity is exhausted.
func TestQueueCapacityBackpressureIsNonBlocking(t *testing.T) {
	release := make(chan struct{})
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srvA.Close()
	defer close(release)

	p := newTestProducer(t)
	cfgCap := cap(p.queue)
	// No Start: dispatcher not running, so the queue stays full.
	for i := 0; i < cfgCap+50; i++ {
		p.publishEvent(Event{URL: srvA.URL}) // must not block
	}
	if len(p.queue) != cfgCap {
		t.Fatalf("queue length = %d, want %d (global bound)", len(p.queue), cfgCap)
	}
}

// TestWorkerCancellationStopsRetries proves ctx cancellation aborts the
// retry loop rather than looping in the background.
func TestWorkerCancellationStopsRetries(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	p := newTestProducer(t)
	p.maxAttempts = 100 // would retry forever without cancellation
	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()

	p.processEventContext(ctx, Event{Type: EventTypeThingChanged, URL: srv.URL})
	n := attempts.Load()
	if n == 0 || n >= 100 {
		t.Fatalf("attempts = %d; want bounded retry loop cancelled by ctx", n)
	}
}

// TestShutdownWithPendingWorkIsGraceful proves cancellation stops accepting
// work and in-flight deliveries finish within their request timeout.
func TestShutdownWithPendingWorkIsGraceful(t *testing.T) {
	var completed atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		completed.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := newTestProducer(t)
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)
	for i := 0; i < 10; i++ {
		p.publishEvent(Event{URL: srv.URL})
	}
	time.Sleep(20 * time.Millisecond)
	cancel()

	idle := make(chan struct{})
	go func() { p.waitForIdle(); close(idle) }()
	select {
	case <-idle:
	case <-time.After(5 * time.Second):
		t.Fatal("waitForIdle did not return after shutdown")
	}
	if completed.Load() == 0 {
		t.Fatal("no in-flight deliveries finished during graceful shutdown")
	}
}

// TestMultipleTenantsDeliverConcurrently exercises fair concurrent fan-out.
func TestMultipleTenantsDeliverConcurrently(t *testing.T) {
	counts := map[string]*atomic.Int32{}
	urls := map[string]string{}
	var mu sync.Mutex
	for _, name := range []string{"a", "b", "c", "d"} {
		var c atomic.Int32
		mu.Lock()
		srv := httptest.NewServer(countHandler(&c))
		urls[name] = srv.URL
		counts[name] = &c
		mu.Unlock()
		t.Cleanup(srv.Close)
	}

	p := newTestProducer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	for i := 0; i < 25; i++ {
		for _, name := range []string{"a", "b", "c", "d"} {
			p.publishEvent(Event{URL: urls[name]})
		}
	}
	waitFor(t, "all tenants fully delivered", func() bool {
		for _, c := range counts {
			if c.Load() < 25 {
				return false
			}
		}
		return true
	})
}

// TestDeliveryPanicDoesNotKillCapacity: a payload whose marshalling panics
// must be contained by the worker's recover; subsequent deliveries still run.
func TestDeliveryPanicDoesNotKillCapacity(t *testing.T) {
	var good atomic.Int32
	srv := httptest.NewServer(countHandler(&good))
	defer srv.Close()

	p := newTestProducer(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	p.publishEvent(Event{URL: srv.URL, Data: panicData{}})
	p.publishEvent(Event{URL: srv.URL, Data: map[string]any{"ok": true}})
	waitFor(t, "delivery after a panicking event", func() bool { return good.Load() >= 1 })
}

type panicData struct{}

func (panicData) MarshalJSON() ([]byte, error) { panic("boom") }

// ---- retry semantics -------------------------------------------------------

func countHandler(counter *atomic.Int32) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		counter.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}
}

func TestRetryTransientThenSuccess(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if attempts.Add(1) < 2 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()

	p := newTestProducer(t)
	var sleeps []time.Duration
	p.sleepF = func(_ context.Context, d time.Duration) error { sleeps = append(sleeps, d); return nil }
	p.processEvent(Event{URL: srv.URL})
	if attempts.Load() != 2 {
		t.Fatalf("attempts = %d, want 2", attempts.Load())
	}
	if len(sleeps) != 1 {
		t.Fatalf("backoff sleeps = %v, want exactly one", sleeps)
	}
}

func TestPermanentClientErrorNotRetried(t *testing.T) {
	for _, code := range []int{http.StatusBadRequest, http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		var attempts atomic.Int32
		code := code
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			attempts.Add(1)
			w.WriteHeader(code)
		}))
		p := newTestProducer(t)
		p.sleepF = func(_ context.Context, d time.Duration) error { t.Fatal("permanent error must not sleep"); return nil }
		p.processEvent(Event{URL: srv.URL})
		srv.Close()
		if attempts.Load() != 1 {
			t.Fatalf("status %d: attempts = %d, want single attempt", code, attempts.Load())
		}
	}
}

func TestMaxAttemptsBounded(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer srv.Close()

	p := newTestProducer(t)
	p.maxAttempts = 4
	p.sleepF = func(_ context.Context, _ time.Duration) error { return nil }
	p.processEvent(Event{URL: srv.URL})
	if got := attempts.Load(); got != 4 {
		t.Fatalf("attempts = %d, want exactly 4 (bounded)", got)
	}
}

func TestRetryAfterHeaderHonoredAndClamped(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts.Add(1)
		w.Header().Set("Retry-After", "3600") // absurdly large; must clamp to BackoffMax
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	p := newTestProducer(t)
	p.backoffMax = 500 * time.Millisecond
	var gotDelay time.Duration
	p.sleepF = func(_ context.Context, d time.Duration) error { gotDelay = d; return nil }
	p.processEvent(Event{URL: srv.URL})
	if gotDelay > p.backoffMax {
		t.Fatalf("delay %v exceeded BackoffMax despite Retry-After clamping", gotDelay)
	}
}

func TestBackoffDeterministicAndCapped(t *testing.T) {
	initial := 1 * time.Second
	max := 30 * time.Second
	cases := []struct {
		attempt int
		frac    float64
		want    time.Duration
	}{
		{0, 0.5, initial},
		{0, 0.0, initial - time.Duration(0.2 * float64(initial))},
		{0, 1.0, initial + time.Duration(0.2 * float64(initial))},
		{3, 0.5, 8 * time.Second},
		{10, 0.5, max},
	}
	for _, tc := range cases {
		got := backoffDelay(initial, max, tc.attempt, tc.frac)
		if got != tc.want {
			t.Errorf("backoff(attempt=%d, frac=%.1f) = %v, want %v", tc.attempt, tc.frac, got, tc.want)
		}
	}
}

// TestPrivateChoreSuppressionStillEnforced re-checks the Phase-7 boundary
// after the Phase-8 dispatcher rewrite.
func TestPrivateChoreSuppressionStillEnforced(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(countHandler(&hits))
	defer srv.Close()

	p := newTestProducer(t)
	url := srv.URL
	private := &chModel.Chore{ID: 7, IsPrivate: true}
	user := &uModel.User{ID: 2, Username: "u"}
	p.ChoreCreated(context.Background(), &url, private, user)
	p.ChoreCompleted(context.Background(), &url, private, user)
	p.ChoreSkipped(context.Background(), &url, private, user)
	if got := len(p.queue); got != 0 {
		t.Fatalf("private chore events queued: %d", got)
	}
}

// TestBackoffReleasesGlobalSlot proves a retrying tenant does not hold the
// global delivery capacity while sleeping between attempts: with Workers=1,
// a second tenant must still be deliverable during tenant A's backoff.
func TestBackoffReleasesGlobalSlot(t *testing.T) {
	var aAttempts, bHits atomic.Int32
	srvA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		aAttempts.Add(1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srvA.Close()
	srvB := httptest.NewServer(countHandler(&bHits))
	defer srvB.Close()

	p := newTestProducer(t)
	p.sem = make(chan struct{}, 1) // single global delivery slot
	p.maxAttempts = 4
	inBackoff := make(chan struct{}, 8)
	var once sync.Once
	p.sleepF = func(ctx context.Context, d time.Duration) error {
		once.Do(func() { close(inBackoff) })
		<-inBackoff // stay parked in backoff for the whole assertion window
		return ctx.Err()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	p.publishEvent(Event{URL: srvA.URL})
	waitFor(t, "tenant A entering first backoff", func() bool {
		select {
		case <-inBackoff:
			return true
		default:
			return false
		}
	})

	// While A parks in backoff holding no global slot, B must deliver.
	p.publishEvent(Event{URL: srvB.URL})
	waitFor(t, "tenant B delivery during A's backoff", func() bool { return bHits.Load() >= 1 })

	if aAttempts.Load() < 1 {
		t.Fatal("tenant A never attempted a delivery")
	}
	cancel()
}
