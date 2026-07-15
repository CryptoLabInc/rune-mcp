// Package lifecycle manages rune-mcp boot sequence + state machine.
// Spec: docs/v04/spec/components/rune-mcp.md §부팅 시퀀스 + §상태 머신.
// Python: mcp/server/server.py main() + _init_pipelines + RunMCPServer.
//
// State machine:
//
//	(spawn) → starting ──(Console OK)──→ active ←──┐
//	              ↓                      ↓       │
//	              └─(Console fail)→ waiting_for_console │
//	                                     ↕       │
//	                                /rune:deactivate
//	                                     ↕       │
//	                                   dormant ──┘
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
	bootLog     *BootLogger  // on-disk failure log; nil = disabled (no-op)
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
	if !m.state.CompareAndSwap(int32(StateActive), int32(StateStarting)) &&
		!m.state.CompareAndSwap(int32(StateDormant), int32(StateStarting)) {
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

// BootBackoffs — Python server.py Console retry schedule.
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
// production deployment (spec/components/embedder.md §불변 계약). The Console
// manifest does not currently carry a dim field; once embedder.Info is
// available end-to-end, the boot loop should source dim from there instead.
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
)

// RunBootLoop drives the boot sequence per spec/components/rune-mcp.md §부팅
// 시퀀스. It runs to completion (Active or Dormant terminal) then returns.
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
			slog.Info("boot: dormant — awaiting /rune:configure or /rune:reload_pipelines",
				"reason", m.LastError())
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
			// to config.json so the next boot picks up the same reason
			// (Python parity: server.py _set_dormant_with_reason).
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
	// Python parity: server.py:L1544 — `if rune_config.state != "active":
	// return result`. Strict check covers all non-active values uniformly.
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

	if cfg.Console.Endpoint == "" || cfg.Console.Token == "" {
		// Config exists but Console credentials are missing. Same UX as missing
		// config — user must run /rune:configure. No retry. Persist to disk
		// so the next boot picks up the same dormant_reason.
		m.SetState(StateDormant)
		m.lastError.Store("console endpoint or token missing in config — run /rune:configure")
		m.SetBootError(ClassifyDormantReason("console_unconfigured"))
		if dErr := config.MarkDormant("console_unconfigured"); dErr != nil {
			slog.Warn("boot: failed to persist dormant state to config.json", "err", dErr)
		}
		slog.Warn("boot: console endpoint/token missing — entering dormant",
			"hint", "run /rune:configure")
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

	consoleClient, err := console.NewClient(cfg.Console.Endpoint, cfg.Console.Token, console.ClientOpts{
		CACertPath: cfg.Console.CACert,
		TLSDisable: cfg.Console.TLSDisable,
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
	// §9.1 B1: reject a malformed bundle at the point of receipt, before the
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

	// §9.2 C1 (2026-07-12 개정): every insert carries the RMP+MM dual
	// representation — the engine, the console proto, and the SDK all reject an
	// MM-less item — so a cluster-less (flat-only) runespace is an unsupported
	// deployment, not a mode. An empty centroid_set_version means either that,
	// or the console could not reach the engine while building the manifest;
	// both block capture, so fail the boot loudly here instead of activating
	// into a state where every capture would fail. The boot loop retries, so a
	// transient engine outage recovers by itself.
	if bundle.CentroidSetVersion == "" {
		m.SetState(StateWaitingForConsole)
		msg := "console manifest carries no centroid_set_version — runespace has no cluster tier (flat-only, unsupported) or the console cannot reach the engine"
		m.lastError.Store(msg)
		m.SetBootError(&domain.BootError{Kind: domain.BootErrConsoleManifest, Detail: msg, Hint: "Configure the runespace cluster tier (centroid set) and check console→runespace connectivity."})
		slog.Error("boot: " + msg)
		_ = consoleClient.Close()
		return bootRetry
	}
	keyDir, err := keymanager.SaveEncKeys(bundle.KeyID, bundle.EncKeyJSON, bundle.MMEncKey)
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
	// windows and diagnostics stay truthful (§ finding: closed-client refs).
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

	// §9.1 B4: the manifest's dim (which also sized the encryptor keys — Open
	// above enforces key/dim agreement) must match the vectors runed actually
	// produces, or captures would encrypt wrong-sized vectors. Best-effort:
	// while runed is still bootstrapping, Info may fail or report 0 — skip
	// rather than block the boot (the bootstrap wait covers that window).
	var runedCentroidVer string
	if info, ierr := embedderClient.Info(ctx); ierr == nil {
		runedCentroidVer = info.CentroidSetVersion
		if info.VectorDim > 0 && info.VectorDim != bundle.Dim {
			msg := fmt.Sprintf("dim mismatch: manifest/keys=%d, runed=%d — console and embedder disagree on the embedding dimension", bundle.Dim, info.VectorDim)
			m.SetState(StateWaitingForConsole)
			m.lastError.Store(msg)
			m.SetBootError(&domain.BootError{Kind: domain.BootErrConfigInvalid, Detail: msg, Hint: "Check that the console's embedding_dim matches the runed model."})
			slog.Error("boot: " + msg)
			return bootRetry
		}
	}

	// Relay the IVF centroid set from the console down to runed so Embed can
	// route inserts. Best-effort at boot: a failure here does not block
	// activation — capture self-heals at the point of use (§9.2 C4: EmbedRoute
	// FAILED_PRECONDITION → the service resyncs the set and retries once) —
	// but a healthy path pushes it now so the first capture pays no resync.
	//
	// Skip when runed already holds the manifest's exact set: Info just told
	// us its version, and re-relaying it costs a ~16MB console fetch + a ~16MB
	// runed push per session boot for nothing. A cold runed (empty version)
	// or any mismatch still takes the full relay, and the C3/C4 self-heal
	// keeps covering post-boot centroid replacement.
	if runedCentroidVer != "" && runedCentroidVer == bundle.CentroidSetVersion {
		slog.Info("boot: centroid relay skipped — runed already holds the current set", "version", runedCentroidVer)
	} else if cs, err := consoleClient.Centroids(ctx); err != nil {
		slog.Warn("boot: centroid relay fetch failed (routing unavailable until retry)", "err", err)
	} else if cs != nil && cs.Version != "" && len(cs.Vectors) > 0 {
		if err := embedderClient.SetCentroids(ctx, cs.Version, cs.Dim, cs.Preset, cs.Vectors); err != nil {
			slog.Warn("boot: centroid push to runed failed", "err", err)
		} else {
			slog.Info("boot: centroid set synced to runed", "version", cs.Version, "nlist", len(cs.Vectors))
		}
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
