package realtime

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/alamoudimoh/daydan-server/config"
)

func newTicketStoreForTest(max int) *TicketStore {
	cfg := &config.Config{RealTimeConfig: config.RealTimeConfig{MaxSSETickets: max}}
	return NewTicketStore(cfg)
}

// TestTicketIsSingleUse pins replay protection: the first consume wins, every
// subsequent consume of the same ticket fails.
func TestTicketIsSingleUse(t *testing.T) {
	s := newTicketStoreForTest(10)
	ticket, err := s.Issue(1, map[string]interface{}{"id": 1})
	if err != nil {
		t.Fatal(err)
	}
	entry, ok := s.Consume(ticket)
	if !ok || entry.userID != 1 {
		t.Fatalf("first consume must succeed, got ok=%v entry=%+v", ok, entry)
	}
	for i := 0; i < 3; i++ {
		if _, ok := s.Consume(ticket); ok {
			t.Fatalf("replay %d: consumed ticket must be rejected", i+2)
		}
	}
}

// TestConcurrentTicketConsumptionSingleWinner proves with a hard barrier that
// two racing consumers cannot both successfully consume one single-use
// ticket. Run under -race in CI.
func TestConcurrentTicketConsumptionSingleWinner(t *testing.T) {
	s := newTicketStoreForTest(10)
	const workers = 16
	ticket, err := s.Issue(7, nil)
	if err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	var winners sync.WaitGroup // counts successful consumers
	winners.Add(1)
	results := make(chan bool, workers)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start // barrier: everyone consumes at the same moment
			_, consumed := s.Consume(ticket)
			if consumed {
				winners.Done()
			}
			results <- consumed
		}()
	}
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("consumers deadlocked")
	}
	close(results)

	successes := 0
	for ok := range results {
		if ok {
			successes++
		}
	}
	// Wait for the winner counter as well; both views must agree.
	winnersWaited := make(chan struct{})
	go func() { winners.Wait(); close(winnersWaited) }()
	<-winnersWaited

	if successes != 1 {
		t.Fatalf("exactly one consumer may win the ticket race, got %d", successes)
	}
}

func TestTicketExpiryAndMalformedInputRejected(t *testing.T) {
	s := newTicketStoreForTest(10)
	if _, ok := s.Consume(""); ok {
		t.Fatal("empty ticket must be rejected")
	}
	if _, ok := s.Consume("not-a-real-ticket"); ok {
		t.Fatal("unknown ticket must be rejected")
	}
	if _, ok := s.Consume(string(make([]byte, 4096))); ok {
		t.Fatal("oversized junk ticket must be rejected")
	}

	// Expired tickets are rejected even though they were never consumed.
	expired := &TicketStore{
		tickets: map[string]ticketEntry{
			"expired": {userID: 1, expiresAt: time.Now().UTC().Add(-time.Minute)},
		},
		maxTickets: 10,
		stopCh:     make(chan struct{}),
	}
	if _, ok := expired.Consume("expired"); ok {
		t.Fatal("expired ticket must be rejected")
	}
	if _, ok := expired.Consume("expired"); ok {
		t.Fatal("expired ticket must not become valid on second attempt")
	}
}

func TestTicketStoreCapacityBounded(t *testing.T) {
	s := newTicketStoreForTest(2)
	if _, err := s.Issue(1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Issue(1, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Issue(1, nil); !errors.Is(err, ErrTicketStoreFull) {
		t.Fatalf("expected ErrTicketStoreFull at capacity, got %v", err)
	}
	// Free space via consumption and continue issuing.
	if _, ok := s.Consume(firstTicket(t, s)); !ok {
		t.Fatal("expected live ticket")
	}
	if _, err := s.Issue(1, nil); err != nil {
		t.Fatalf("issue after consuming should work: %v", err)
	}
}

func firstTicket(t *testing.T, s *TicketStore) string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	for ticket := range s.tickets {
		return ticket
	}
	t.Fatal("store unexpectedly empty")
	return ""
}

// TestRealtimeCredentialsAreNotInterchangeable documents the boundary that
// realtime tickets only ever authenticate /sse: an access-token-shaped or
// API-token-shaped string presented where a ticket belongs is just an unknown
// key and can never resolve to a user.
func TestRealtimeTicketsAreScopedToTheTicketStore(t *testing.T) {
	ctx := context.Background()
	_ = ctx
	h := newRealtimeHarness(t, time.Second)

	_, tokens := h.login(t, "scope-user")

	// Access token used AS a ticket: unknown to the store.
	if _, ok := h.ticketStore().Consume(tokens.AccessToken); ok {
		t.Fatal("access token must not be consumable as an SSE ticket")
	}
	// A minted ticket is not a JWT/API credential: it is random hex bound to
	// this store only, and it disappears after consumption (single use).
	ticket, err := h.ticketStore().Issue(1, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ticket) != 64 {
		t.Fatalf("ticket must be 256-bit hex, got %d chars", len(ticket))
	}
}
