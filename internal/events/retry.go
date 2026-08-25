package events

import (
	"context"
	"net/http"
	"strconv"
	"time"
)

// Delivery contract (bounded, at-least-once per event while the process
// lives; NOT exactly-once network delivery):
//
//   2xx                      → success
//   408 / 429 / 5xx          → transient, retried
//   other 4xx                → permanent, no retry (receiver rejected it)
//   network/timeout errors   → transient, retried
//
// Retries use exponential backoff with ±20% jitter capped at BackoffMax.
// A Retry-After header is honored when present and sane, clamped to
// BackoffMax. After MaxAttempts total attempts the event is abandoned and
// logged — Stripe-style durable redelivery is out of scope for outbound
// webhooks; consumers who need guaranteed delivery should acknowledge with
// 2xx and queue internally.

// outcome classifies a single delivery attempt.
type outcome int

const (
	outcomeSuccess outcome = iota
	outcomeTransient
	outcomePermanent
)

func classifyStatus(code int) outcome {
	switch {
	case code >= 200 && code < 300:
		return outcomeSuccess
	case code == http.StatusRequestTimeout, code == http.StatusTooManyRequests, code >= 500:
		return outcomeTransient
	default:
		return outcomePermanent
	}
}

// parseRetryAfter extracts a bounded delay from a Retry-After header
// (delta-seconds or HTTP-date). Returns 0 when absent or unusable.
func parseRetryAfter(header string, now time.Time, cap time.Duration) time.Duration {
	if header == "" {
		return 0
	}
	if secs, err := strconv.Atoi(header); err == nil {
		d := time.Duration(secs) * time.Second
		if d < 0 {
			return 0
		}
		return minDuration(d, cap)
	}
	if when, err := http.ParseTime(header); err == nil {
		d := when.Sub(now)
		if d <= 0 {
			return 0
		}
		return minDuration(d, cap)
	}
	return 0
}

// backoffDelay computes the jittered exponential delay before attempt
// number `attempt` (0-based). Deterministic given the injected rand source;
// production uses math/rand's global source via jitterFraction.
func backoffDelay(initial, max time.Duration, attempt int, frac float64) time.Duration {
	d := initial
	for i := 0; i < attempt; i++ {
		d *= 2
		if d >= max {
			break
		}
	}
	if d > max {
		d = max
	}
	// ±20% jitter around d: result = d * (0.8 + 0.4*frac), clamped to max so
	// the +20% tail never exceeds the configured ceiling.
	out := time.Duration(float64(d) * (0.8 + 0.4*frac))
	if out > max {
		out = max
	}
	return out
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// sleepBackoff waits for d, aborting early on ctx cancellation. Returns
// ctx.Err() if cancelled.
func sleepBackoff(ctx context.Context, sleep func(context.Context, time.Duration) error, d time.Duration) error {
	if sleep == nil {
		timer := time.NewTimer(d)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	return sleep(ctx, d)
}
