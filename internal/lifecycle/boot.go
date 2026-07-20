// Package lifecycle manages rune-mcp boot sequence + state machine.
//
// State machine:
//
//	(spawn) → starting ──(Console OK, runed model ready)──→ active ←──┐
//	              ↓            │                              ↑        │
//	              │            └─(model still downloading)→ waiting_for_bootstrap
//	              │                       (async watcher retriggers when ready) │
//	              ↓                                                   │
//	              └─(Console fail)→ waiting_for_console               │
//	                                     ↕                            │
//	                                /rune:deactivate                  │
//	                                     ↕                            │
//	                                   dormant ────────────────────────┘
//	                                /rune:activate
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"sync/atomic"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/config"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/keymanager"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/runespacecrypto"
	"github.com/CryptoLabInc/rune-mcp/internal/domain"
	"github.com/CryptoLabInc/rune-mcp/internal/recovery"
)

// BootAdapterInjector decouples lifecycle from mcp.Deps to break the
// adapter ↔ handler import cycle. The boot loop pushes adapter clients +
// per-token Console bundle metadata through this interface; the concrete
// implementation (mcp.Deps) propagates them onto the 3 service structs.
type BootAdapterInjector interface {
	InjectConsole(client console.Client)
	InjectEmbedder(client embedder.Client)
	InjectEncryptor(enc Encryptor)
	ApplyConsoleBundle(bundle *console.Bundle)
}

// Encryptor is the client-side FHE encrypt surface the boot loop opens from
// the manifest's EncKey and injects. A superset of service.Encryptor (the
// encrypt methods) plus Close, because the boot loop owns the handle's
// lifecycle: the cgo key context behind it is invisible to the GC and is
// released only by Close — on replacement (re-boot) and at process exit.
// Declared here to avoid a lifecycle->service import cycle.
type Encryptor interface {
	EncryptFlat(vec []float32) ([]byte, error)
	EncryptClustered(vec []float32) ([]byte, error)
	Close() error
}

// State — atomic-safe enum.
type State int32

const (
	StateStarting State = iota
	StateWaitingForConsole
	StateActive
	StateDormant
	// StateWaitingForBootstrap — the data plane is fully wired (console dialed,
	// keys saved, encryptor open, embedder dialed, centroid set pushed to
	// runed) and only runed's embedding model is still downloading. Distinct
	// from Starting/WaitingForConsole (nothing is wrong and the console is
	// reachable) and from Active (Embed cannot run until the model loads, so
	// capture/recall stay gated). An async watcher polls runed and retriggers
	// the boot loop once the model is ready, which then reaches Active.
	StateWaitingForBootstrap
)

func (s State) String() string {
	switch s {
	case StateStarting:
		return "starting"
	case StateWaitingForConsole:
		return "waiting_for_console"
	case StateActive:
		return "active"
	case StateDormant:
		return "dormant"
	case StateWaitingForBootstrap:
		return "waiting_for_bootstrap"
	}
	return "unknown"
}

// Manager — atomic state + Console boot loop control.
type Manager struct {
	state       atomic.Int32
	lastError   atomic.Value // string — free-form, kept for slog parity
	lastBootErr atomic.Value // *domain.BootError — structured, surfaced via diagnostics
	attempts    atomic.Int32
	onReload    atomic.Value // func()
	// onBootstrapWatch — invoked once the boot loop settles into
	// StateWaitingForBootstrap, to start the async goroutine that polls runed
	// until its model finishes downloading and then Retriggers the loop. Wired
	// by cmd/rune-mcp (→ LifecycleService.StartBootstrapWatcher); mirrors the
	// onReload indirection so lifecycle stays free of a service import.
	onBootstrapWatch atomic.Value // func()
	bootLog          *BootLogger  // on-disk failure log; nil = disabled (no-op)
}

// NewManager — initial state = Starting.
func NewManager() *Manager {
	m := &Manager{}
	m.state.Store(int32(StateStarting))
	// Seed lastBootErr with a typed nil so atomic.Value.Load returns a
	// consistent type after the first SetBootError call (atomic.Value
	// requires all stored values to be the same concrete type).
	m.lastBootErr.Store((*domain.BootError)(nil))
	return m
}

// SetBootLog injects the on-disk boot-failure logger. Called once during wiring
// (cmd/rune-mcp). Leave unset (nil) to disable disk persistence — SetBootError
// then only updates in-memory state. Not safe to call concurrently with boot.
func (m *Manager) SetBootLog(bl *BootLogger) { m.bootLog = bl }

// BootLog returns the injected logger (nil when disabled). Exposed so the
// shutdown path can add it to the Closer list.
func (m *Manager) BootLog() *BootLogger { return m.bootLog }

// Current — atomic load.
func (m *Manager) Current() State {
	return State(m.state.Load())
}

// SetState — atomic store.
func (m *Manager) SetState(s State) {
	m.state.Store(int32(s))
}

// SetReloadFunc installs the callback that respawns the boot loop. main.go
// wires this so service.LifecycleService.ReloadPipelines can ask for a
// fresh attempt without taking a circular import dependency on
// lifecycle.RunBootLoop.
//
// The callback should spawn a fresh RunBootLoop goroutine bound to the
// long-lived ctx + the same Deps (BootAdapterInjector). Manager itself
// does not invoke the callback unless Retrigger is called and state is
// Dormant — see Retrigger.
func (m *Manager) SetReloadFunc(f func()) {
	m.onReload.Store(f)
}

// SetBootstrapWatchFunc installs the callback the boot loop fires when it
// settles into StateWaitingForBootstrap (runed reachable + centroids pushed,
// model still downloading). The callback should start an idempotent goroutine
// that polls runed and calls Retrigger once the model is ready. Wired by
// cmd/rune-mcp to LifecycleService.StartBootstrapWatcher; the service layer
// owns the poll because only it holds the embedder client.
func (m *Manager) SetBootstrapWatchFunc(f func()) {
	m.onBootstrapWatch.Store(f)
}

// fireBootstrapWatch invokes the bootstrap-watch callback if one is wired.
// No-op when unset (tests that don't wire a watcher).
func (m *Manager) fireBootstrapWatch() {
	v := m.onBootstrapWatch.Load()
	if v == nil {
		return
	}
	if f, ok := v.(func()); ok && f != nil {
		f()
	}
}

// Retrigger respawns the boot loop only if no loop is currently running and
// only one caller wins when called concurrently
// Transitioning Active to Starting (or Dormant to Starting) atomically claims
// right to spawn RunBootLoop
func (m *Manager) Retrigger() {
	v := m.onReload.Load()
	if v == nil {
		return
	}

	f, ok := v.(func())
	if !ok || f == nil {
		return
	}

	// Atomically claim the right to spawn. Losers fall through and return.
	// WaitingForBootstrap is included: the boot loop has ALREADY EXITED into
	// that state (handing off to the async watcher), so no loop is running and
	// the watcher's Retrigger — or a manual /rune:activate — must be able to
	// respawn it. WaitingForConsole/Starting are deliberately excluded: a loop
	// is actively retrying there and a second one must not be spawned.
	if !m.state.CompareAndSwap(int32(StateActive), int32(StateStarting)) &&
		!m.state.CompareAndSwap(int32(StateDormant), int32(StateStarting)) &&
		!m.state.CompareAndSwap(int32(StateWaitingForBootstrap), int32(StateStarting)) {
		return
	}
	f()
}

// LastError reports the most recent transient failure recorded by the boot
// loop (empty string when none). Used by diagnostics tools.
func (m *Manager) LastError() string {
	v := m.lastError.Load()
	if v == nil {
		return ""
	}
	s, _ := v.(string)
	return s
}

const RecoverTimeout = 30 * time.Second

func (m *Manager) WaitForActive(ctx context.Context, timeout time.Duration) bool {
	if m == nil {
		return false
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(500 * time.Millisecond):
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		switch m.Current() {
		case StateActive:
			return true
		case StateDormant:
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(100 * time.Millisecond):
		}
	}
	return false
}

// LastBootError reports the structured boot error from the most recent boot
// attempt (nil when boot is currently active / has not yet been attempted /
// has explicitly been cleared on success). Surfaced via
// service.LifecycleService.Diagnostics so agents can fast-fail on a stable
// Kind + Hint instead of pattern-matching LastError() strings.
func (m *Manager) LastBootError() *domain.BootError {
	v := m.lastBootErr.Load()
	if v == nil {
		return nil
	}
	be, _ := v.(*domain.BootError)
	return be
}

// SetBootError stores a classified boot error in atomic state AND appends
// it to the on-disk boot log (~/.rune/logs/boot.log). nil is treated as
// "clear" — atomic is reset, log is left intact (past attempts remain
// inspectable).
func (m *Manager) SetBootError(be *domain.BootError) {
	// atomic.Value requires consistent concrete type — store typed nil
	// rather than an untyped nil interface{} for the clear case.
	if be == nil {
		m.lastBootErr.Store((*domain.BootError)(nil))
		return
	}
	// Inline-constructed BootErrors (as opposed to ClassifyBootError output)
	// carry no timestamp; stamp here so every stored/persisted entry has one.
	if be.At.IsZero() {
		be.At = time.Now().UTC()
	}
	m.lastBootErr.Store(be)
	// Best-effort persist. Persist is nil-safe and swallows file errors so
	// this can never break the boot loop.
	m.bootLog.Persist(be)
}

// Attempts reports the cumulative retry count from the most recent boot run
// (reset to 0 each time RunBootLoop starts). Exposed for diagnostics.
func (m *Manager) Attempts() int {
	return int(m.attempts.Load())
}

// BootBackoffs — Console retry schedule.
// Total to cap: 1s → 2s → 5s → 15s → 30s → 60s (then loop at 60s).
var BootBackoffs = []time.Duration{
	1 * time.Second,
	2 * time.Second,
	5 * time.Second,
	15 * time.Second,
	30 * time.Second,
	60 * time.Second,
}

// DefaultKeyDim is the FHE slot dimension matching the Qwen3-Embedding-0.6B
// production deployment. The Console manifest does not currently carry a dim
// field; once embedder.Info is available end-to-end, the boot loop should
// source dim from there instead.
const DefaultKeyDim = 1024

// bootResult is the outcome of one bootOnce attempt.
type bootResult int

const (
	// bootRetry — transient failure (Console unreachable, network blip, partial
	// init error). Caller should backoff and try again.
	bootRetry bootResult = iota

	// bootActive — full success: Console dialed, manifest parsed, keys persisted,
	// adapters wired, services injected. Caller should set StateActive and exit.
	bootActive

	// bootDormant — terminal: config missing, config.State="dormant", or console
	// endpoint/token unconfigured. Retrying won't help — only /rune:configure
	// (or a process restart) will. Caller should set StateDormant and exit;
	// service.LifecycleService.ReloadPipelines is responsible for re-spawning
	// RunBootLoop after the user fixes config.
	bootDormant

	// bootWaitingBootstrap — the data plane is fully wired and the centroid set
	// is pushed to runed, but runed's embedding model is still downloading.
	// State is already set to StateWaitingForBootstrap inside bootOnce. The
	// caller should NOT retry-spin (that would re-wire the plane while the
	// download runs); instead it fires the bootstrap-watch callback and exits.
	// The watcher Retriggers RunBootLoop once the model is ready, which then
	// skips the centroid relay (version match) and reaches bootActive.
	bootWaitingBootstrap
)

// RunBootLoop drives the boot sequence. It runs to completion (Active or
// Dormant terminal) then returns.
// Re-init after dormant↔active transitions is the responsibility of
// service.LifecycleService.ReloadPipelines (which spawns a fresh RunBootLoop
// goroutine).
//
// Failure modes:
//   - config.json missing             → terminal Dormant (await /rune:configure)
//   - config.State="dormant"          → terminal Dormant (user explicit)
//   - console endpoint/token empty      → terminal Dormant (await /rune:configure)
//   - console dial / GetAgentManifest   → state=WaitingForConsole, exp backoff retry
//   - keymanager / embedder / encryptor init → exp backoff retry (might be
//     transient — daemon down, etc.)
//   - runed reachable + centroids pushed but model still downloading →
//     state=WaitingForBootstrap, fire the bootstrap watcher, exit (no retry)
//   - other config error (parse fail) → exp backoff retry (user might be editing)
//   - ctx cancellation                → return immediately
//
// Every attempt that fails after a successful Console dial closes the partial
// adapter conns it created (console, embedder) before retrying so
// gRPC connections do not leak across retries.
func RunBootLoop(ctx context.Context, m *Manager, deps BootAdapterInjector) {
	m.SetState(StateStarting)
	m.attempts.Store(0)
	// Don't clear lastBootErr here — keep the previous run's error visible
	// until this run produces a new outcome. That way a manual /rune:status
	// during the first ~second of a Retrigger still shows context.

	attempt := 0
	for {
		if ctx.Err() != nil {
			return
		}
		m.attempts.Store(int32(attempt))

		switch bootOnce(ctx, m, deps, attempt) {
		case bootActive:
			m.SetState(StateActive)
			m.lastError.Store("")
			m.SetBootError(nil)
			m.attempts.Store(int32(attempt))
			slog.Info("boot: pipelines initialized and active")
			return

		case bootDormant:
			// State + lastError + lastBootErr already set inside bootOnce.
			m.attempts.Store(int32(attempt))
			slog.Info("boot: dormant — awaiting /rune:configure or /rune:activate",
				"reason", m.LastError())
			return

		case bootWaitingBootstrap:
			// Data plane wired + centroids pushed; only runed's model download
			// is outstanding. State already set to StateWaitingForBootstrap in
			// bootOnce. Hand off to the async watcher and exit — do NOT retry
			// (the watcher Retriggers this loop when the model is ready).
			m.attempts.Store(int32(attempt))
			slog.Info("boot: data plane wired — awaiting runed model download",
				"reason", m.LastError())
			m.fireBootstrapWatch()
			return

		case bootRetry:
			if attempt > 0 && attempt%20 == 0 {
				slog.Error("boot: persistent failure — check config or network",
					"attempt", attempt,
					"last_error", m.LastError())
			}
			sleepBackoff(ctx, attempt)
			attempt++
		}
	}
}

// bootOnce runs one boot attempt. Returns:
//   - bootActive  on full success
//   - bootDormant on terminal config-side failures (caller should not retry)
//   - bootRetry   on transient failures (caller backs off and retries)
//
// On any failure path, state + lastError + lastBootErr are updated.
//
// Adapter injection is per-component, not all-or-nothing.
func bootOnce(ctx context.Context, m *Manager, deps BootAdapterInjector, attempt int) bootResult {
	cfg, err := config.Load()
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			// Fresh install — config.json not provisioned. Retrying won't help;
			// user must run /rune:configure first. Persist the dormant state
			// to config.json so the next boot picks up the same reason.
			m.SetState(StateDormant)
			m.lastError.Store("config.json not found — run /rune:configure to set up")
			m.SetBootError(ClassifyDormantReason("not_configured"))
			if dErr := config.MarkDormant("not_configured"); dErr != nil {
				slog.Warn("boot: failed to persist dormant state to config.json", "err", dErr)
			}
			slog.Warn("boot: config.json not found — entering dormant",
				"hint", "run /rune:configure")
			return bootDormant
		}
		// Other config errors (JSON parse, permission denied, etc.) — could be
		// transient (user editing the file). Retry.
		m.lastError.Store(fmt.Sprintf("config load: %v", err))
		m.SetBootError(ClassifyBootError(err, BootErrCtx{
			Phase:    domain.BootPhaseConfigLoad,
			Attempts: attempt,
		}))
		slog.Error("boot: failed to load config", "err", err)
		return bootRetry
	}

	// Anything other than config.State == "active" is treated as dormant:
	//   - "dormant"        — user explicitly deactivated (or a previous boot
	//                         persisted dormant via MarkDormant)
	//   - ""               — fresh install or hand-edited config without state
	//   - other / unknown  — corrupted config
	//
	// The strict check covers all non-active values uniformly.
	// /rune:activate transitions config.State back to "active" and re-spawns
	// RunBootLoop.
	if cfg.State != "active" {
		m.SetState(StateDormant)

		var reason string
		switch cfg.State {
		case "dormant":
			reason = cfg.DormantReason
			if reason == "" {
				reason = "user_deactivated"
			}
		case "":
			reason = "not_configured"
		default:
			reason = "invalid_state"
		}

		m.lastError.Store("dormant: " + reason)
		m.SetBootError(ClassifyDormantReason(reason))
		if dErr := config.MarkDormant(reason); dErr != nil {
			slog.Warn("boot: failed to persist dormant state to config.json", "err", dErr)
		}
		slog.Info("boot: state != active — staying dormant",
			"config.state", cfg.State,
			"reason", reason)
		return bootDormant
	}

	// The token may live in the OS keyring (TokenStorage=keyring); resolve it up
	// front so the credential gate and the dial below share one value.
	token, tokErr := cfg.ResolveToken()
	if cfg.Console.Endpoint == "" || tokErr != nil || token == "" {
		// Config exists but Console credentials are missing or unreadable (e.g.
		// keyring says keyring-stored but the entry is gone / keyring locked).
		// Same UX as missing config — user must run /rune:configure. No retry.
		m.SetState(StateDormant)
		msg := "console endpoint or token missing in config — run /rune:configure"
		if tokErr != nil {
			msg = "console token unavailable: " + tokErr.Error()
		}
		m.lastError.Store(msg)
		m.SetBootError(ClassifyDormantReason("console_unconfigured"))
		if dErr := config.MarkDormant("console_unconfigured"); dErr != nil {
			slog.Warn("boot: failed to persist dormant state to config.json", "err", dErr)
		}
		slog.Warn("boot: console credentials unavailable — entering dormant",
			"hint", "run /rune:configure", "detail", msg)
		return bootDormant
	}

	// classify — helper closure that classifies an error with the current
	// phase + interpolates the user's endpoint/CA path into the hint. Pulled
	// out so each error site stays a single readable line.
	classify := func(err error, phase domain.BootPhase) *domain.BootError {
		return ClassifyBootError(err, BootErrCtx{
			Phase:           phase,
			ConsoleEndpoint: cfg.Console.Endpoint,
			ConsoleCAPath:   cfg.Console.CACert,
			Attempts:        attempt,
		})
	}

	consoleClient, err := console.NewClient(cfg.Console.Endpoint, token, console.ClientOpts{
		CACertPath: cfg.Console.CACert,
		UnaryInterceptors: []grpc.UnaryClientInterceptor{
			recovery.UnaryRecovery("console", m),
		},
	})
	if err != nil {
		m.SetState(StateWaitingForConsole)
		m.lastError.Store(fmt.Sprintf("console dial: %v", err))
		m.SetBootError(classify(err, domain.BootPhaseConsoleDial))
		slog.Error("boot: failed to connect to console", "err", err)
		return bootRetry
	}

	bundle, err := consoleClient.GetAgentManifest(ctx)
	if err != nil {
		m.SetState(StateWaitingForConsole)
		m.lastError.Store(fmt.Sprintf("console get manifest: %v", err))
		m.SetBootError(classify(err, domain.BootPhaseConsoleManifest))
		slog.Warn("boot: waiting for console...", "err", err)
		_ = consoleClient.Close()
		return bootRetry
	}

	// Persist the PUBLIC EncKey pair from the manifest and open the local
	// encryptor (cgo). Capture encrypts here so plaintext vectors never reach
	// the console; SecKey stays in the console, so this opens Enc-only.
	//
	// Reject a malformed bundle at the point of receipt, before the
	// bad material overwrites the on-disk key copies or — worse — boots into
	// active and fails far away (a missing agent_dek only surfaces at the
	// first capture's seal step otherwise). Format-level checks only; real
	// cryptographic validity is judged where the material is used
	// (runespacecrypto.Open for keys, the console's openMeta for the dek).
	if msg := validateBundle(bundle); msg != "" {
		m.SetState(StateWaitingForConsole)
		m.lastError.Store(msg)
		m.SetBootError(&domain.BootError{Kind: domain.BootErrConsoleManifest, Detail: msg, Hint: "The console answered with an incomplete manifest — check the console's version and key state."})
		slog.Error("boot: " + msg)
		_ = consoleClient.Close()
		return bootRetry
	}

	// An empty centroid_set_version means the workspace isn't wired to a
	// runespace yet, or Console couldn't fetch the set while building the
	// manifest. Every insert needs the RMP+MM dual representation (engine,
	// console proto, and SDK all reject an MM-less item), so with no set every
	// capture would fail — wait and retry instead of activating into that state.
	// The boot loop retries, so a transient outage recovers on its own.
	if bundle.CentroidSetVersion == "" {
		m.SetState(StateWaitingForConsole)
		msg := "console has not provided a centroid set yet — the rune workspace may not be connected to Console yet, or the set could not be fetched"
		m.lastError.Store(msg)
		m.SetBootError(&domain.BootError{Kind: domain.BootErrConsoleManifest, Detail: msg, Hint: "Boot retries automatically as the workspace connects. If this persists, contact your Console administrator."})
		slog.Error("boot: " + msg)
		_ = consoleClient.Close()
		return bootRetry
	}
	keyDir, err := keymanager.SaveEncKeys(bundle.KeyID, bundle.RMPEncKey, bundle.MMEncKey)
	if err != nil {
		m.lastError.Store(fmt.Sprintf("save enc keys: %v", err))
		m.SetBootError(&domain.BootError{Kind: domain.BootErrKeySave, Detail: err.Error(), Hint: "Check ~/.rune/keys is writable."})
		slog.Error("boot: failed to save EncKey", "err", err)
		_ = consoleClient.Close()
		return bootRetry
	}
	enc, err := runespacecrypto.Open(keyDir, bundle.KeyID, bundle.Dim)
	if err != nil {
		m.lastError.Store(fmt.Sprintf("open encryptor: %v", err))
		m.SetBootError(&domain.BootError{Kind: domain.BootErrRunespaceInit, Detail: err.Error(), Hint: "The EncKey may be corrupt; re-run /rune:configure."})
		slog.Error("boot: failed to open encryptor", "err", err)
		_ = consoleClient.Close()
		return bootRetry
	}
	// Share the clients only now — after every gate (capability, bundle
	// validation, centroid guard, key save, encryptor open) has passed. The
	// failure exits above close a client nothing else references yet, so the
	// services keep the previous boot's still-live console client during retry
	// windows and diagnostics stay truthful.
	deps.InjectConsole(consoleClient)
	deps.ApplyConsoleBundle(bundle)
	deps.InjectEncryptor(enc)

	embedderClient, err := embedder.New(embedder.ResolveSocketPath(""), embedder.Opts{
		UnaryInterceptors: []grpc.UnaryClientInterceptor{
			recovery.UnaryRecovery("embedder", m),
		},
	})
	if err != nil {
		m.lastError.Store(fmt.Sprintf("embedder dial: %v", err))
		m.SetBootError(classify(err, domain.BootPhaseEmbedderDial))
		slog.Error("boot: failed to connect to embedder", "err", err)
		return bootRetry
	}
	deps.InjectEmbedder(embedderClient)

	// The manifest's dim (which also sized the encryptor keys — Open
	// above enforces key/dim agreement) must match the vectors runed actually
	// produces, or captures would encrypt wrong-sized vectors. Best-effort:
	// while runed is still bootstrapping, Info may fail or report 0 — skip
	// rather than block the boot (the bootstrap wait covers that window).
	//
	// One Info failure is NOT tolerated: gRPC Unavailable means there is no
	// transport at all (socket missing, daemon not running), so the centroid
	// push below would fail the same way. Fail fast here, BEFORE the ~16MB
	// console fetch — otherwise every retry of a stuck loop re-downloads the
	// whole set just to fail at the push.
	var runedCentroidVer string
	info, ierr := embedderClient.Info(ctx)
	switch {
	case ierr == nil:
		runedCentroidVer = info.CentroidSetVersion
		if info.VectorDim > 0 && info.VectorDim != bundle.Dim {
			msg := fmt.Sprintf("dim mismatch: manifest/keys=%d, runed=%d — console and embedder disagree on the embedding dimension", bundle.Dim, info.VectorDim)
			m.SetState(StateWaitingForConsole)
			m.lastError.Store(msg)
			m.SetBootError(&domain.BootError{Kind: domain.BootErrConfigInvalid, Detail: msg, Hint: "Check that the console's embedding_dim matches the runed model."})
			slog.Error("boot: " + msg)
			return bootRetry
		}
	case status.Code(ierr) == codes.Unavailable:
		m.SetState(StateWaitingForConsole)
		m.lastError.Store(fmt.Sprintf("embedder unreachable: %v", ierr))
		m.SetBootError(classify(ierr, domain.BootPhaseEmbedderDial))
		slog.Error("boot: embedder unreachable — retrying without fetching centroids", "err", ierr)
		return bootRetry
	}

	// Relay the IVF centroid set from the console down to runed so Embed can
	// route inserts. This is a HARD boot gate: runed must actually hold the
	// current set before we activate. Activation self-reports the member as
	// "online" to the console (below), and online must mean the data plane can
	// route right now — not "up but silently unable to route until the first
	// capture self-heals". A relay failure keeps the boot in WaitingForConsole
	// and retries, exactly like any other console-side gap; the capture-time
	// resync (EmbedRoute FAILED_PRECONDITION → the service resyncs and retries
	// once) still covers post-boot centroid replacement while active.
	//
	// Skip only when runed already holds the manifest's exact set: Info just
	// told us its version, and re-relaying it costs a ~16MB console fetch + a
	// ~16MB runed push per session boot for nothing. A cold runed (empty
	// version) or any mismatch takes the full relay.
	if runedCentroidVer != "" && runedCentroidVer == bundle.CentroidSetVersion {
		slog.Info("boot: centroid relay skipped — runed already holds the current set", "version", runedCentroidVer)
	} else {
		cs, err := consoleClient.Centroids(ctx)
		if err != nil {
			m.SetState(StateWaitingForConsole)
			m.lastError.Store(fmt.Sprintf("centroid relay fetch: %v", err))
			m.SetBootError(classify(err, domain.BootPhaseRunespaceIndex))
			slog.Error("boot: centroid relay fetch failed — cannot activate until runed holds the set", "err", err)
			return bootRetry
		}
		if cs == nil || cs.Version == "" || len(cs.Vectors) == 0 {
			m.SetState(StateWaitingForConsole)
			msg := "console returned an empty centroid set"
			m.lastError.Store(msg)
			m.SetBootError(&domain.BootError{Kind: domain.BootErrRunespaceIndex, Detail: msg, Hint: "The runespace has no centroid set to relay yet. Boot retries automatically as it becomes available; if this persists, check console→runespace connectivity with your Console admin."})
			slog.Error("boot: " + msg)
			return bootRetry
		}
		if err := embedderClient.SetCentroids(ctx, cs.Version, cs.Dim, cs.Preset, cs.Vectors); err != nil {
			m.SetState(StateWaitingForConsole)
			m.lastError.Store(fmt.Sprintf("centroid push to runed: %v", err))
			m.SetBootError(classify(err, domain.BootPhaseEmbedderDial))
			slog.Error("boot: centroid push to runed failed — cannot activate", "err", err)
			return bootRetry
		}
		slog.Info("boot: centroid set synced to runed", "version", cs.Version, "nlist", len(cs.Vectors))
	}

	// Gate activation on runed's embedding MODEL being ready — not merely on
	// runed being reachable. SetCentroids above lands the routing table while
	// runed is still downloading its model (the table is independent of the
	// embedding backend — runed's SetCentroids never touches the backend), but
	// Embed cannot run until the model is up. Reporting "online" now would flip
	// the console member to online and let captures through to a backend that
	// answers FAILED_PRECONDITION.
	//
	// So when the model is still loading, settle into WaitingForBootstrap
	// instead of activating: the data plane is fully wired and the centroid set
	// is already pushed, so the async watcher only has to wait out the download
	// and Retrigger this loop — which then skips the relay (version match) and
	// reaches bootActive. capture/recall stay gated until then. Only an explicit
	// LOADING defers; OK/IDLE (model ready — the common warm re-activate) and a
	// health-probe error (runed just answered SetCentroids, so treat a probe
	// miss as ready and let ReportActivation proceed) both fall through to
	// active.
	if h, herr := embedderClient.Health(ctx); herr == nil && h.Status == "LOADING" {
		m.SetState(StateWaitingForBootstrap)
		m.lastError.Store("runed is downloading its embedding model")
		// Not an error — a normal transient wait. Clear any prior boot error so
		// diagnostics don't surface a stale failure while we wait.
		m.SetBootError(nil)
		slog.Info("boot: runed model still downloading — deferring activation to async watcher",
			"phase", h.Phase, "bytes_done", h.BytesDone, "bytes_total", h.BytesTotal)
		return bootWaitingBootstrap
	}

	// Self-report terminal activation so the console flips the member from
	// invite_redeemed to online. Reached only after every configure step above
	// succeeded — most importantly, runed holds the current centroid set AND its
	// model is ready — so "online" truthfully means the data plane can route.
	// The call itself stays best-effort: if this one RPC fails the member simply
	// stays invite_redeemed until a later boot re-reports, and the local data
	// plane is already fully up.
	if err := consoleClient.ReportActivation(ctx); err != nil {
		slog.Warn("boot: report activation to console failed", "err", err)
	}

	return bootActive
}

// sleepBackoff sleeps for BootBackoffs[min(attempt, len-1)] but returns
// early if ctx is cancelled.
func sleepBackoff(ctx context.Context, attempt int) {
	idx := attempt
	if idx >= len(BootBackoffs) {
		idx = len(BootBackoffs) - 1
	}
	select {
	case <-time.After(BootBackoffs[idx]):
	case <-ctx.Done():
	}
}
