package notifier

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alamoudimoh/dydan-server/config"
)

// TestSchedulerStopsOnCancel replaces the old non-cancellable time.Sleep loop:
// cancellation must terminate the loop even between ticks.
func TestSchedulerStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	s := &Scheduler{}
	exited := make(chan struct{})
	go func() {
		s.runScheduler(ctx, " TEST_JOB ", func(context.Context) (time.Duration, error) { return 0, nil }, time.Hour)
		close(exited)
	}()
	cancel()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("runScheduler did not exit within 2s of cancellation (would strand a goroutine per restart)")
	}
}

// TestSchedulerStopsDuringSleep proves a pending inter-run wait is
// cancellable: cancel while the loop waits for its next tick (interval far in
// the future) and require prompt exit — impossible with time.Sleep.
func TestSchedulerStopsDuringSleep(t *testing.T) {
	s := &Scheduler{}
	exited := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		s.runScheduler(ctx, " TEST_JOB ", func(context.Context) (time.Duration, error) { return 0, nil }, 24*time.Hour)
		close(exited)
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("cancellation during inter-run wait did not stop the scheduler")
	}
}

// TestSchedulerJobPanicDoesNotKillLoop: one panicking tick must not take down
// the scheduler; the next tick still runs.
func TestSchedulerJobPanicDoesNotKillLoop(t *testing.T) {
	var runs atomic.Int32
	s := &Scheduler{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() {
		s.runScheduler(ctx, " PANICKY ", func(context.Context) (time.Duration, error) {
			if runs.Add(1) == 1 {
				panic("boom")
			}
			return 0, nil
		}, 10*time.Millisecond)
		close(done)
	}()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if runs.Load() >= 2 {
			cancel()
			select {
			case <-done:
			case <-time.After(time.Second):
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("scheduler died after panicking job; runs=%d", runs.Load())
}

// TestSchedulerRunsAreSequential: a job slower than its interval must never
// overlap itself (duplicate execution guard).
func TestSchedulerRunsAreSequential(t *testing.T) {
	var concurrent, maxConcurrent int32
	var mu sync.Mutex
	job := func(context.Context) (time.Duration, error) {
		mu.Lock()
		concurrent++
		if concurrent > maxConcurrent {
			maxConcurrent = concurrent
		}
		mu.Unlock()
		time.Sleep(30 * time.Millisecond)
		mu.Lock()
		concurrent--
		mu.Unlock()
		return 0, nil
	}
	s := &Scheduler{}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		s.runScheduler(ctx, " SLOW ", job, 1*time.Millisecond)
		close(done)
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	if maxConcurrent > 1 {
		t.Fatalf("job overlapped itself: max concurrency %d", maxConcurrent)
	}
}

// TestStopIsIdempotentAndSafeWithoutStart: Stop must be callable multiple
// times and before Start (the old unbuffered stopChan send would deadlock).
func TestStopIsIdempotentAndSafeWithoutStart(t *testing.T) {
	s := NewScheduler(&config.Config{}, nil, nil, nil, nil, nil, nil)
	done := make(chan struct{})
	go func() {
		s.Stop()
		s.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop deadlocked")
	}
}

// TestStartStopLifecycle: full start → stop cycle terminates both worker
// loops within the bounded shutdown window.
func TestStartStopLifecycle(t *testing.T) {
	s := NewScheduler(&config.Config{}, nil, nil, nil, nil, nil, nil)
	s.Start(context.Background())
	stopped := make(chan struct{})
	go func() {
		s.Stop()
		close(stopped)
	}()
	select {
	case <-stopped:
	case <-time.After(5 * time.Second):
		t.Fatal("Stop did not return after full Start/Stop cycle")
	}
}
