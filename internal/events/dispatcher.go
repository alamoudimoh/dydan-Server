package events

import (
	"context"
)

// tenantBucket is the per-destination FIFO with single-flight delivery.
//
// Isolation model: every distinct webhook destination gets its own bounded
// queue and at most one delivery in flight. A slow or failing tenant can
// therefore only ever occupy its own slot plus its own bucket — never the
// delivery capacity owed to other tenants. Global concurrency across all
// tenants is capped by the producer's worker semaphore.
type tenantBucket struct {
	key     string
	pending []Event
	busy    bool
}

func (b *tenantBucket) full(limit int) bool { return len(b.pending) >= limit }
func (b *tenantBucket) pop() Event {
	ev := b.pending[0]
	b.pending = b.pending[1:]
	return ev
}

// maxTenantBuckets caps the number of distinct destinations tracked by the
// dispatcher so a pathological workload of unique destinations cannot grow
// bookkeeping without bound; beyond it events are dropped with a warning,
// matching the global queue's explicit-drop backpressure semantics.
const maxTenantBuckets = 4096

// dispatchLoop consumes the inbound queue into per-tenant buckets and hands
// events to bounded workers. Every state change (new queued event, tenant
// slot released) signals wake, so the loop never spins and never deadlocks:
// drops happen instead of blocking publishes.
func (p *EventsProducer) dispatchLoop(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-p.queue:
			if !ok {
				return
			}
			p.enqueueTenant(ev)
		case <-p.wake:
		}

		// Drain everything already buffered so bursts don't pay one wakeup
		// per event, then start a worker for every idle tenant with work.
		p.drainInbound()
		if err := p.launchClaimable(ctx); err != nil {
			return
		}
	}
}

// drainInbound moves queued events into their tenant buckets without
// blocking. The inbound channel remains the global memory bound.
func (p *EventsProducer) drainInbound() {
	for {
		select {
		case ev, ok := <-p.queue:
			if !ok {
				return
			}
			p.enqueueTenant(ev)
		default:
			return
		}
	}
}

// enqueueTenant appends an event to its destination's bucket under the
// per-tenant cap. When a tenant's bucket is full the NEWEST event is
// dropped: backpressure must be explicit and bounded — never unbounded
// buffering, and blocking publishers would let one slow tenant stall
// request handlers.
func (p *EventsProducer) enqueueTenant(event Event) {
	limit := p.perTenantLimit()
	p.mu.Lock()
	defer p.mu.Unlock()
	b, ok := p.byKey[event.URL]
	if !ok {
		if len(p.buckets) >= maxTenantBuckets {
			p.dropEvent(event, "tenant table full")
			return
		}
		b = &tenantBucket{key: event.URL}
		p.buckets = append(p.buckets, b)
		p.byKey[event.URL] = b
	}
	if b.full(limit) {
		p.dropEvent(event, "tenant queue full")
		return
	}
	b.pending = append(b.pending, event)
	p.notify()
}

func (p *EventsProducer) pruneLocked(key string) {
	b, ok := p.byKey[key]
	if !ok || b.busy || len(b.pending) != 0 {
		return
	}
	delete(p.byKey, key)
	for i, bb := range p.buckets {
		if bb == b {
			p.buckets = append(p.buckets[:i], p.buckets[i+1:]...)
			break
		}
	}
	if p.cursor >= len(p.buckets) && len(p.buckets) > 0 {
		p.cursor = 0
	}
}

func (p *EventsProducer) dropEvent(event Event, reason string) {
	if p.logger != nil {
		p.logger.Warnw("Webhook event dropped", "reason", reason,
			"type", string(event.Type), "url", redacted(event.URL))
	}
}

// releaseTenant frees a destination's single delivery slot and wakes the
// dispatcher so queued follow-up work can start immediately.
func (p *EventsProducer) releaseTenant(key string) {
	p.mu.Lock()
	if b, ok := p.byKey[key]; ok {
		b.busy = false
		p.pruneLocked(key)
	}
	p.mu.Unlock()
	p.notify()
}

// beginInflight registers an in-flight delivery under idleMu so graceful
// shutdown can wait for it without racing the dispatcher's accounting.
func (p *EventsProducer) beginInflight() {
	p.idleMu.Lock()
	p.inflight++
	p.idleMu.Unlock()
}

func (p *EventsProducer) endInflight() {
	p.idleMu.Lock()
	p.inflight--
	if p.inflight <= 0 {
		p.idleCond.Broadcast()
	}
	p.idleMu.Unlock()
}

// launchClaimable starts one worker per idle bucket holding pending work.
// The global semaphore bounds total fan-out at Workers regardless of how
// many tenants have work. Returns an error only when ctx is cancelled.
func (p *EventsProducer) launchClaimable(ctx context.Context) error {
	for {
		event, key, ok := p.claimNext()
		if !ok {
			return nil
		}
		select {
		case p.sem <- struct{}{}:
		case <-ctx.Done():
			p.releaseTenant(key)
			return ctx.Err()
		}
		p.beginInflight()
		go func(event Event, key string) {
			defer func() {
				if r := recover(); r != nil {
					// One broken delivery must never take down the worker
					// pool or another tenant's delivery capacity.
					if p.logger != nil {
						p.logger.Errorw("Webhook delivery panicked", "url", redacted(key), "panic", r)
					}
				}
			}()
			defer p.endInflight()
			defer func() { <-p.sem }()
			defer p.releaseTenant(key)
			ops := &slotOps{
				acquire: func(acqCtx context.Context) bool {
					select {
					case p.sem <- struct{}{}:
						return true
					case <-acqCtx.Done():
						return false
					}
				},
				release: func() { <-p.sem },
			}
			p.deliverWithRetriesSlot(ctx, event, ops)
		}(event, key)
	}
}

// claimNext round-robins over tenant buckets and claims the next event from
// an idle one, enforcing exactly one in-flight delivery per tenant.
func (p *EventsProducer) claimNext() (Event, string, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n := len(p.buckets)
	for i := 0; i < n; i++ {
		b := p.buckets[(p.cursor+i)%n]
		if b.busy || len(b.pending) == 0 {
			continue
		}
		b.busy = true
		ev := b.pop()
		p.cursor = (p.cursor + i + 1) % n
		if len(b.pending) == 0 {
			p.pruneLocked(b.key)
		}
		return ev, b.key, true
	}
	return Event{}, "", false
}

// WaitForIdle blocks until no deliveries are in flight. Exported for the
// server lifecycle's graceful-shutdown sequence.
func (p *EventsProducer) WaitForIdle() { p.waitForIdle() }

// waitForIdle blocks until no deliveries are in flight. Used by graceful
// shutdown.
func (p *EventsProducer) waitForIdle() {
	p.idleMu.Lock()
	for p.inflight > 0 {
		p.idleCond.Wait()
	}
	p.idleMu.Unlock()
}
