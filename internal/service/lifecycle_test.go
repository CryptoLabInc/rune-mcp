package service

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	"github.com/CryptoLabInc/rune-mcp/internal/lifecycle"
)

type stubEmbedder struct { // Health only embedder
	healthFn func(context.Context) (embedder.HealthSnapshot, error)
}

func (s *stubEmbedder) EmbedSingle(context.Context, string) ([]float32, error) {
	return nil, nil
}
func (s *stubEmbedder) EmbedBatch(context.Context, []string) ([][]float32, error) {
	return nil, nil
}
func (s *stubEmbedder) Info(context.Context) (embedder.InfoSnapshot, error) {
	return embedder.InfoSnapshot{}, nil
}
func (s *stubEmbedder) Health(ctx context.Context) (embedder.HealthSnapshot, error) {
	return s.healthFn(ctx)
}
func (s *stubEmbedder) SocketPath() string { return "" }
func (s *stubEmbedder) Close() error       { return nil }

// Apply faster polling interval for test
func withFastWatchInterval(t *testing.T, d time.Duration) {
	t.Helper()
	prev := bootstrapWatchInterval
	bootstrapWatchInterval = d
	t.Cleanup(func() { bootstrapWatchInterval = prev })
}

func newWatcherTestService(t *testing.T, healthFn func(context.Context) (embedder.HealthSnapshot, error)) (*LifecycleService, *stubEmbedder, chan struct{}) {
	t.Helper()
	stub := &stubEmbedder{healthFn: healthFn}
	mgr := lifecycle.NewManager()

	mgr.SetState(lifecycle.StateDormant) // Set dormant state for Retrigger

	fired := make(chan struct{}, 1)
	mgr.SetReloadFunc(func() {
		select {
		case fired <- struct{}{}:
		default:
		}
	})

	s := &LifecycleService{
		State: mgr,
	}
	s.SetEmbedder(stub)

	return s, stub, fired
}

func waitFor(ch <-chan struct{}, timeout time.Duration) bool {
	select {
	case <-ch:
		return true
	case <-time.After(timeout):
		return false
	}
}

// Predicate until returns true or timeout
func waitForState(predicate func() bool, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if predicate() {
			return true
		}
		time.Sleep(time.Millisecond)
	}

	return predicate()
}

// Happy path
func TestBootstrapWatcher_RetriggersOnHealthOK(t *testing.T) {
	withFastWatchInterval(t, 5*time.Millisecond)

	var calls atomic.Int32
	healthFn := func(context.Context) (embedder.HealthSnapshot, error) {
		n := calls.Add(1)
		if n < 3 {
			return embedder.HealthSnapshot{Status: "LOADING"}, nil
		}
		return embedder.HealthSnapshot{Status: "OK"}, nil
	}
	s, _, fired := newWatcherTestService(t, healthFn)

	s.startBootstrapWatcher()

	if !waitFor(fired, time.Second) {
		t.Fatalf("Retrigger never fired (Health calls=%d)", calls.Load())
	}
	if !waitForState(func() bool {
		s.bootstrapWatcherMu.Lock()
		defer s.bootstrapWatcherMu.Unlock()
		return !s.bootstrapWatcherRunning
	}, time.Second) {
		t.Errorf("watcher should have exited after firing Retrigger")
	}
}

func TestBootstrapWatcher_Idempotent(t *testing.T) {
	withFastWatchInterval(t, 5*time.Millisecond)

	var calls atomic.Int32
	healthFn := func(context.Context) (embedder.HealthSnapshot, error) {
		calls.Add(1)
		return embedder.HealthSnapshot{Status: "LOADING"}, nil // LOADING forever to make watcher keep running
	}
	s, _, _ := newWatcherTestService(t, healthFn)

	s.startBootstrapWatcher()

	if !waitForState(func() bool {
		return calls.Load() > 0
	}, time.Second) {
		t.Fatal("first watcher never polled")
	}

	for i := 0; i < 5; i++ {
		s.startBootstrapWatcher()
	}

	time.Sleep(50 * time.Millisecond) // Wait for other watchers

	s.bootstrapWatcherMu.Lock()
	running := s.bootstrapWatcherRunning
	s.bootstrapWatcherMu.Unlock()
	if !running {
		t.Errorf("watcher should still be running (LOADING perpetual)")
	}

	// TODO: rely on Go garbage collector since we don't have watcher stop API for now
}

// --- Error cases ---//
func TestBootstrapWatcher_ExitsOnDegraded(t *testing.T) {
	withFastWatchInterval(t, 5*time.Millisecond)

	healthFn := func(context.Context) (embedder.HealthSnapshot, error) {
		return embedder.HealthSnapshot{Status: "DEGRADED"}, nil
	}
	s, _, fired := newWatcherTestService(t, healthFn)

	s.startBootstrapWatcher()

	if !waitForState(func() bool {
		s.bootstrapWatcherMu.Lock()
		defer s.bootstrapWatcherMu.Unlock()
		return !s.bootstrapWatcherRunning
	}, time.Second) {
		t.Fatalf("watcher should have exited on DEGRADED")
	}

	select {
	case <-fired:
		t.Errorf("Retrigger should NOT fire on DEGRADED")
	case <-time.After(50 * time.Millisecond):
		// expected: no fire
	}
}

func TestBootstrapWatcher_ExitsOnHealthError(t *testing.T) {
	withFastWatchInterval(t, 5*time.Millisecond)

	healthFn := func(context.Context) (embedder.HealthSnapshot, error) {
		return embedder.HealthSnapshot{}, errors.New("connection refused")
	}
	s, _, fired := newWatcherTestService(t, healthFn)

	s.startBootstrapWatcher()

	if !waitForState(func() bool {
		s.bootstrapWatcherMu.Lock()
		defer s.bootstrapWatcherMu.Unlock()
		return !s.bootstrapWatcherRunning
	}, time.Second) {
		t.Fatalf("watcher should have exited on Health error")
	}
	select {
	case <-fired:
		t.Errorf("Retrigger should NOT fire on Health error")
	case <-time.After(50 * time.Millisecond):
	}
}
