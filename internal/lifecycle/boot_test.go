package lifecycle

import (
	"sync/atomic"
	"testing"
)

// TestState_String covers every State value, including the new
// waiting_for_bootstrap. The strings are a surfaced contract (diagnostics /
// activate status / ReloadPipelinesResult.State), so pin them.
func TestState_String(t *testing.T) {
	cases := []struct {
		s    State
		want string
	}{
		{StateStarting, "starting"},
		{StateWaitingForConsole, "waiting_for_console"},
		{StateActive, "active"},
		{StateDormant, "dormant"},
		{StateWaitingForBootstrap, "waiting_for_bootstrap"},
		{State(99), "unknown"},
	}
	for _, tc := range cases {
		if got := tc.s.String(); got != tc.want {
			t.Errorf("State(%d).String() = %q, want %q", tc.s, got, tc.want)
		}
	}
}

// TestRetrigger_RespawnsFromWaitingForBootstrap — the bootstrap watcher's
// Retrigger (and a manual /rune:activate) must be able to respawn the boot loop
// once the model download finishes. The loop has already EXITED into
// waiting_for_bootstrap, so no loop is running and a respawn is correct.
func TestRetrigger_RespawnsFromWaitingForBootstrap(t *testing.T) {
	m := NewManager()
	var fired atomic.Int32
	m.SetReloadFunc(func() { fired.Add(1) })

	m.SetState(StateWaitingForBootstrap)
	m.Retrigger()

	if fired.Load() != 1 {
		t.Fatalf("reload func fired %d times, want 1 (respawn from waiting_for_bootstrap)", fired.Load())
	}
	// Retrigger claims the transition via CAS to Starting before calling f().
	if got := m.Current(); got != StateStarting {
		t.Errorf("state after Retrigger = %v, want starting", got)
	}
}

// TestRetrigger_NoRespawnWhileLoopRunning — WaitingForConsole and Starting mean
// a boot loop is actively retrying; Retrigger must be a no-op so a second loop
// is never spawned.
func TestRetrigger_NoRespawnWhileLoopRunning(t *testing.T) {
	for _, running := range []State{StateStarting, StateWaitingForConsole} {
		m := NewManager()
		var fired atomic.Int32
		m.SetReloadFunc(func() { fired.Add(1) })

		m.SetState(running)
		m.Retrigger()

		if fired.Load() != 0 {
			t.Errorf("state %v: reload func fired %d times, want 0", running, fired.Load())
		}
		if got := m.Current(); got != running {
			t.Errorf("state %v: changed to %v after no-op Retrigger", running, got)
		}
	}
}

// TestFireBootstrapWatch — the callback fires when wired and is a safe no-op
// when unset (tests / early boot before wiring).
func TestFireBootstrapWatch(t *testing.T) {
	m := NewManager()

	// Unset: must not panic.
	m.fireBootstrapWatch()

	var fired atomic.Int32
	m.SetBootstrapWatchFunc(func() { fired.Add(1) })
	m.fireBootstrapWatch()
	if fired.Load() != 1 {
		t.Fatalf("bootstrap-watch callback fired %d times, want 1", fired.Load())
	}
}
