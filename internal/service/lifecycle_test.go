package service

import (
	"context"
	"errors"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/config"
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
func (s *stubEmbedder) EmbedRoute(context.Context, string) (embedder.Routed, error) {
	return embedder.Routed{}, nil
}
func (s *stubEmbedder) SetCentroids(context.Context, string, int, string, [][]float32) error {
	return nil
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

// --- bootstrapProgress (read-only snapshot for the activate response) ---//

func TestBootstrapProgress_NilEmbedder(t *testing.T) {
	s := &LifecycleService{State: lifecycle.NewManager()}
	if d := s.bootstrapProgress(context.Background()); d != nil {
		t.Errorf("nil embedder: want nil BootstrapDetail, got %+v", d)
	}
}

func TestBootstrapProgress_ReturnsDownloadDetail(t *testing.T) {
	stub := &stubEmbedder{healthFn: func(context.Context) (embedder.HealthSnapshot, error) {
		return embedder.HealthSnapshot{
			Status:     "LOADING",
			Phase:      "FETCHING_MODEL",
			BytesDone:  512,
			BytesTotal: 2048,
			Message:    "downloading",
		}, nil
	}}
	s := &LifecycleService{State: lifecycle.NewManager()}
	s.SetEmbedder(stub)

	d := s.bootstrapProgress(context.Background())
	if d == nil {
		t.Fatal("want BootstrapDetail, got nil")
	}
	if d.Phase != "FETCHING_MODEL" || d.BytesDone != 512 || d.BytesTotal != 2048 || d.Message != "downloading" {
		t.Errorf("unexpected detail: %+v", d)
	}
}

func TestBootstrapProgress_HealthErrorIsNil(t *testing.T) {
	stub := &stubEmbedder{healthFn: func(context.Context) (embedder.HealthSnapshot, error) {
		return embedder.HealthSnapshot{}, errors.New("unreachable")
	}}
	s := &LifecycleService{State: lifecycle.NewManager()}
	s.SetEmbedder(stub)

	if d := s.bootstrapProgress(context.Background()); d != nil {
		t.Errorf("health error: want nil BootstrapDetail, got %+v", d)
	}
}

// --- Activate: waiting_for_bootstrap short-circuit is health-gated ---//

// writeActiveConfig provisions a minimal ~/.rune/config.json (under the
// test-scoped $HOME) so Activate gets past its configure_required pre-checks.
func writeActiveConfig(t *testing.T) {
	t.Helper()
	cfg := &config.Config{State: "active"}
	cfg.Console.Endpoint = "127.0.0.1:1"
	cfg.Console.Token = "test-token"
	if err := config.Save(cfg); err != nil {
		t.Fatalf("write config: %v", err)
	}
}

// isolateActivateEnv scopes HOME (config), the runed socket, and every rune
// binary lookup path to empty test dirs, so Activate's ensureDaemon path
// deterministically resolves to install_pending instead of spawning anything.
func isolateActivateEnv(t *testing.T) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("RUNE_EMBEDDER_SOCKET", filepath.Join(t.TempDir(), "embedding.sock"))
	t.Setenv("RUNE_HOME", t.TempDir())
	t.Setenv("CLAUDE_PLUGIN_ROOT", "")
	t.Setenv("PATH", "")
}

// While runed reports LOADING, Activate must short-circuit: report progress,
// keep the watcher alive, and NOT re-run the boot loop.
func TestActivate_WaitingForBootstrap_LoadingReportsProgress(t *testing.T) {
	isolateActivateEnv(t)
	writeActiveConfig(t)

	mgr := lifecycle.NewManager()
	mgr.SetState(lifecycle.StateWaitingForBootstrap)
	s := &LifecycleService{State: mgr}
	s.SetEmbedder(&stubEmbedder{healthFn: func(context.Context) (embedder.HealthSnapshot, error) {
		return embedder.HealthSnapshot{Status: "LOADING", Phase: "FETCHING_MODEL", BytesDone: 1, BytesTotal: 2}, nil
	}})

	res, err := s.Activate(context.Background())
	if err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if res.Status != ActivateStatusWaitingForBootstrap {
		t.Fatalf("status: got %q want %q", res.Status, ActivateStatusWaitingForBootstrap)
	}
	if res.Bootstrap == nil || res.Bootstrap.Phase != "FETCHING_MODEL" {
		t.Errorf("bootstrap detail: got %+v, want FETCHING_MODEL progress", res.Bootstrap)
	}
	if got := mgr.Current(); got != lifecycle.StateWaitingForBootstrap {
		t.Errorf("state: got %v, want unchanged waiting_for_bootstrap", got)
	}
}

// When the state says waiting_for_bootstrap but runed does NOT answer LOADING
// (crashed mid-download, DEGRADED, or the model finished after the watcher's
// deadline), Activate must fall through to the full path instead of promising
// "completes automatically" forever. With no rune binary resolvable in the
// test env, the full path deterministically surfaces install_pending.
func TestActivate_WaitingForBootstrap_NotLoadingFallsThrough(t *testing.T) {
	cases := []struct {
		name     string
		healthFn func(context.Context) (embedder.HealthSnapshot, error)
	}{
		{"runed dead", func(context.Context) (embedder.HealthSnapshot, error) {
			return embedder.HealthSnapshot{}, errors.New("unavailable")
		}},
		{"runed degraded", func(context.Context) (embedder.HealthSnapshot, error) {
			return embedder.HealthSnapshot{Status: "DEGRADED"}, nil
		}},
		{"model ready, watcher expired", func(context.Context) (embedder.HealthSnapshot, error) {
			return embedder.HealthSnapshot{Status: "OK"}, nil
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			isolateActivateEnv(t)
			writeActiveConfig(t)

			mgr := lifecycle.NewManager()
			mgr.SetState(lifecycle.StateWaitingForBootstrap)
			s := &LifecycleService{State: mgr}
			s.SetEmbedder(&stubEmbedder{healthFn: tc.healthFn})

			res, err := s.Activate(context.Background())
			if err != nil {
				t.Fatalf("Activate: %v", err)
			}
			if res.Status == ActivateStatusWaitingForBootstrap {
				t.Fatalf("status: still %q — short-circuit did not fall through", res.Status)
			}
			if res.Status != ActivateStatusInstallPending {
				t.Errorf("status: got %q want %q (full path reached ensureDaemon)", res.Status, ActivateStatusInstallPending)
			}
		})
	}
}
