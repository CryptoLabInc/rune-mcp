package service

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"time"

	"github.com/CryptoLabInc/rune-console/pkg/regstr"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/config"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/keyring"
	"github.com/CryptoLabInc/rune-mcp/internal/domain"
	"github.com/CryptoLabInc/rune-mcp/internal/lifecycle"
	"github.com/CryptoLabInc/rune-mcp/internal/spawn"
)

// LifecycleService holds the lifecycle/operational tool implementations.
type LifecycleService struct {
	Console console.Client
	State   *lifecycle.Manager

	// Key state (for diagnostics). In the runespace model the console is the sole
	// key custodian; mcp holds no key material, only the KeyID from the manifest.
	KeyID string

	bootstrapWatcherMu      sync.Mutex
	bootstrapWatcherRunning bool

	embedderMu sync.RWMutex
	embedder   embedder.Client
}

// NewLifecycleService constructs.
func NewLifecycleService() *LifecycleService {
	return &LifecycleService{}
}

func (s *LifecycleService) Embedder() embedder.Client {
	s.embedderMu.RLock()
	defer s.embedderMu.RUnlock()
	return s.embedder
}

func (s *LifecycleService) SetEmbedder(c embedder.Client) {
	s.embedderMu.Lock()
	defer s.embedderMu.Unlock()
	s.embedder = c
}

// ─────────────────────────────────────────────────────────────────────────────
// rune_diagnostics — read-only.
// ─────────────────────────────────────────────────────────────────────────────

// DiagnosticsResult — aggregates 6 sub-sections (env + runtime ×5). Install
// state (config.json, runed binary, model file, socket) is a substrate
// concern owned by the `rune` CLI; agents wanting that visibility shell
// out to `rune verify` separately. Keeping the MCP server's diagnostics
// scoped to runtime state mirrors the rune ↔ rune-mcp repo boundary.
type DiagnosticsResult struct {
	OK            bool          `json:"ok"`
	Environment   EnvInfo       `json:"environment"`
	State         *string       `json:"state,omitempty"`
	DormantReason *string       `json:"dormant_reason,omitempty"`
	DormantSince  *string       `json:"dormant_since,omitempty"`
	Console       ConsoleInfo   `json:"console"`
	Keys          KeysInfo      `json:"keys"`
	Embedding     EmbeddingInfo `json:"embedding"`
}

// EnvInfo — OS, Go runtime version, cwd.
type EnvInfo struct {
	OS      string `json:"os"`
	Runtime string `json:"runtime"`
	CWD     string `json:"cwd"`
	GOArch  string `json:"goarch"`
}

// ConsoleInfo — subset exposed in diagnostics.
//
// Configured = a Console gRPC client is wired (boot loop reached Active).
// Healthy    = the most recent HealthCheck succeeded.
// Error      = HealthCheck error (operational, set only when Healthy=false).
// LastBootError = structured boot failure from lifecycle.Manager. Surfaces
//
//	the actual reason for waiting_for_console state — agents
//	branch on .kind to fast-fail without manual probing. Nil
//	when boot has succeeded or no attempt has been made yet.
type ConsoleInfo struct {
	Configured    bool              `json:"configured"`
	Healthy       bool              `json:"healthy"`
	Endpoint      string            `json:"endpoint,omitempty"`
	Error         string            `json:"error,omitempty"`
	LastBootError *domain.BootError `json:"last_boot_error,omitempty"`
}

// KeysInfo — key custody status. In the runespace model the console is the sole
// key custodian (EncKey/EvalKey/SecKey never leave it); the mcp process holds
// no key material. Key readiness is not reported here: it has no signal
// independent of console.healthy (the same HealthCheck probe), so callers should
// read console.healthy instead.
type KeysInfo struct {
	Custodian string `json:"custodian"` // "console" — sole key holder
	KeyID     string `json:"key_id,omitempty"`
}

// EmbeddingInfo - external embedder info snapshot
//
// Phase / BytesDone / BytesTotal / Message are populated when Status is
// LOADING
type EmbeddingInfo struct {
	Model         string `json:"model"`
	VectorDim     int    `json:"vector_dim,omitempty"`
	DaemonVersion string `json:"daemon_version,omitempty"`
	SocketPath    string `json:"socket_path,omitempty"`
	Status        string `json:"status,omitempty"` // Health: OK / LOADING / DEGRADED / SHUTTING_DOWN
	UptimeSeconds int64  `json:"uptime_seconds,omitempty"`
	TotalRequests int64  `json:"total_requests,omitempty"`
	Phase         string `json:"phase,omitempty"`       // bootstrap sub-phase; meaningful when Status == LOADING
	BytesDone     int64  `json:"bytes_done,omitempty"`  // download progress
	BytesTotal    int64  `json:"bytes_total,omitempty"` // 0 when unknown / not downloading
	Message       string `json:"message,omitempty"`     // free-text detail for end-user display
	InfoError     string `json:"info_error,omitempty"`
	HealthError   string `json:"health_error,omitempty"`
}

// DiagnosticsTimeout — per-probe deadline for diagnostics HealthCheck calls. 5s.
const DiagnosticsTimeout = 5 * time.Second

// Diagnostics collects all 6 sections + derives top-level OK.
func (s *LifecycleService) Diagnostics(ctx context.Context) *DiagnosticsResult {
	r := &DiagnosticsResult{OK: true}

	// Environment
	cwd, _ := os.Getwd()
	r.Environment = EnvInfo{
		OS:      runtime.GOOS,
		Runtime: runtime.Version(),
		CWD:     cwd,
		GOArch:  runtime.GOARCH,
	}

	// Config state
	cfg, err := config.Load()
	if err == nil && cfg != nil {
		state := cfg.State
		r.State = &state
		if cfg.DormantReason != "" {
			r.DormantReason = &cfg.DormantReason
		}
		if cfg.DormantSince != "" {
			r.DormantSince = &cfg.DormantSince
		}
	}

	// Console
	r.Console = s.collectConsole(ctx, DiagnosticsTimeout)

	// Keys
	r.Keys = KeysInfo{
		Custodian: "console",
		KeyID:     s.KeyID,
	}

	// Embedding
	r.Embedding = s.collectEmbedding(ctx, DiagnosticsTimeout)

	if s.Console != nil && !r.Console.Healthy {
		r.OK = false
	}

	return r
}

func (s *LifecycleService) collectConsole(ctx context.Context, timeout time.Duration) ConsoleInfo {
	info := ConsoleInfo{Configured: s.Console != nil}

	// Surface the most recent boot failure regardless of client state.
	// When the boot loop is stuck on waiting_for_console, s.Console is nil but
	// LastBootError holds the actual reason — agents need this to skip
	// expensive trial-and-error diagnosis.
	if s.State != nil {
		if be := s.State.LastBootError(); be != nil {
			info.LastBootError = be
		}
	}

	if s.Console == nil {
		return info
	}

	info.Endpoint = s.Console.Endpoint()

	ctx2, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	healthy, err := s.Console.HealthCheck(ctx2)
	if err != nil {
		info.Error = err.Error()
	}

	info.Healthy = healthy

	return info
}

func (s *LifecycleService) collectEmbedding(ctx context.Context, timeout time.Duration) EmbeddingInfo {
	info := EmbeddingInfo{}
	e := s.Embedder()
	if e == nil {
		return info
	}
	info.SocketPath = e.SocketPath()

	infoCtx, cancelInfo := context.WithTimeout(ctx, timeout)
	defer cancelInfo()
	if snap, err := e.Info(infoCtx); err != nil {
		info.InfoError = err.Error()
	} else {
		info.Model = snap.ModelIdentity
		info.VectorDim = snap.VectorDim
		info.DaemonVersion = snap.DaemonVersion
	}

	healthCtx, cancelHealth := context.WithTimeout(ctx, timeout)
	defer cancelHealth()

	if health, err := e.Health(healthCtx); err != nil {
		info.HealthError = err.Error()
	} else {
		info.Status = health.Status
		info.UptimeSeconds = health.UptimeSeconds
		info.TotalRequests = health.TotalRequests
		info.Phase = health.Phase
		info.BytesDone = health.BytesDone
		info.BytesTotal = health.BytesTotal
		info.Message = health.Message
	}

	return info
}

// ─────────────────────────────────────────────────────────────────────────────
// rune_configure — write Console credentials to $HOME/.rune/config.json.
// ─────────────────────────────────────────────────────────────────────────────

// ConfigureArgs — the configure tool takes only the registration string; the
// server derives endpoint, token, and CA from it (see bootstrapFromRegistration).
type ConfigureArgs struct {
	RegistrationString string `json:"registration_string" jsonschema:"The one-time runev1_… string from your Rune invite email. This is the only input — the server bootstraps the endpoint, access token, and pinned CA from it."`
}

// resolvedCreds holds the Console credentials the bootstrap derives from a
// registration string.
type resolvedCreds struct {
	Endpoint   string
	Token      string
	CACertPath string
}

type ConfigureResult struct {
	OK           bool   `json:"ok"`
	Path         string `json:"path"`
	State        string `json:"state"`
	ConfiguredAt string `json:"configured_at"`
	NextStep     string `json:"next_step,omitempty"`

	// ConsoleReachable is derived from the boot state after bring-up:
	// nil   — bring-up stopped before the boot loop could probe the console
	//         (runed spawn failed first; NextStep carries the recovery).
	// true  — reached active OR waiting_for_bootstrap (console was dialed and
	//         the centroid set relayed; only runed's model download may remain).
	// false — a classified boot failure; ProbeError carries its detail.
	ConsoleReachable *bool  `json:"console_reachable,omitempty"`
	ProbeError       string `json:"probe_error,omitempty"`
}

func (s *LifecycleService) Configure(ctx context.Context, args ConfigureArgs) (*ConfigureResult, error) {
	if args.RegistrationString == "" {
		return nil, &domain.RuneError{Code: domain.CodeInvalidInput, Message: "registration_string is required"}
	}
	// The server runs the 3-stage bootstrap from the registration string: decode
	// → fetch + pin the CA → unwrap the one-time handle into the real token.
	creds, err := s.bootstrapFromRegistration(ctx, args.RegistrationString)
	if err != nil {
		return nil, err
	}

	cfg, err := config.Load()
	if err != nil {
		cfg = &config.Config{} // fall back to fresh config
	}

	// Reconfigure starts from a clean slate: drop the previous endpoint's
	// keyring token so re-running configure (e.g. with a fresh invite) never
	// orphans a stale secret under an old endpoint key. Best-effort — Delete is
	// a no-op when nothing is stored and non-fatal when the keyring is
	// unavailable.
	if cfg.Console.Endpoint != "" {
		_ = keyring.Delete(cfg.Console.Endpoint)
	}

	// Prefer the OS keyring for the token so it never lands in a plaintext file.
	// Fall back to the config file (0600) when the host has no usable keyring
	// (headless CI, no D-Bus session, locked/denied keychain).
	consoleCfg := config.ConsoleConfig{
		Endpoint: creds.Endpoint,
		CACert:   creds.CACertPath,
	}
	if err := keyring.Set(creds.Endpoint, creds.Token); err != nil {
		if !keyring.IsUnavailable(err) {
			// Post-unwrap: the invite is already spent but the token could not be
			// persisted to the keyring (locked/denied keychain, not merely
			// absent). Same dead end as a config.Save failure — a fresh invite is
			// required once the keyring is fixed. Do not return a generic error
			// the caller would retry with the now-useless string.
			return nil, &domain.RuneError{
				Code:      domain.CodeRegistrationConsumed,
				Message:   fmt.Sprintf("invite redeemed but storing the token in the OS keyring failed: %v", err),
				Retryable: false,
				RecoveryHint: "The one-time invite was consumed by this attempt but the token could not be stored in the OS keyring. " +
					"Unlock/allow the keychain, then request a NEW invite — the same registration string can no longer be reused.",
			}
		}
		slog.Warn("keyring unavailable; storing token in config file (0600)", "err", err.Error())
		consoleCfg.Token = creds.Token
		consoleCfg.TokenStorage = config.TokenStorageConfig
	} else {
		consoleCfg.TokenStorage = config.TokenStorageKeyring
		slog.Info("console token stored in OS keyring", "endpoint", creds.Endpoint)
	}
	cfg.Console = consoleCfg
	cfg.State = "active"
	cfg.DormantReason = ""
	cfg.DormantSince = ""

	now := time.Now().UTC().Format(time.RFC3339)
	if cfg.Metadata == nil {
		cfg.Metadata = map[string]any{}
	}
	cfg.Metadata["lastUpdated"] = now

	if err := config.Save(cfg); err != nil {
		// The invite was already redeemed during bootstrap, so the handle is
		// spent — surface that a fresh invite is needed rather than a generic
		// save error the caller would retry in vain.
		return nil, &domain.RuneError{
			Code:      domain.CodeRegistrationConsumed,
			Message:   fmt.Sprintf("invite redeemed but saving credentials failed: %v", err),
			Retryable: false,
			RecoveryHint: "The one-time invite was consumed by this attempt but credentials could not be written to ~/.rune. " +
				"Resolve the local error (free disk space, fix ~/.rune permissions), then request a NEW invite — the same registration string can no longer be reused.",
		}
	}

	path, _ := config.DefaultConfigPath()
	result := &ConfigureResult{
		OK:           true,
		Path:         path,
		State:        cfg.State,
		ConfiguredAt: now,
	}

	// Credentials are persisted and the one-time invite is fully consumed, so
	// configure owns bringing the pipelines online from here: drive the real boot
	// loop (the same path /rune:activate uses) instead of a throwaway HealthCheck.
	// A follow-up /rune:activate then short-circuits to "already active".
	//
	// Any failure past this point is a connectivity/boot problem, never a
	// spent-invite one — the invite was already redeemed above, so the recovery
	// is /rune:activate or /rune:status and never a fresh invite. (The only
	// spent-invite dead end is a config.Save failure, handled above with
	// CodeRegistrationConsumed before the token could ever take effect.)

	// The boot loop only DIALS runed's socket — it never spawns the daemon, and
	// a first-time configure has no runed running yet. Mirror /rune:activate's
	// pre-check: bring the daemon up before driving the boot loop, or a fresh
	// install can never reach active from configure alone. On failure, surface
	// the recovery hint (install + /rune:activate retry) instead of failing the
	// call — the credentials above are already saved.
	if socketPath := embedder.ResolveSocketPath(""); socketPath != "" {
		if br := s.ensureDaemon(ctx, socketPath); br != nil {
			// Retrigger even though runed could not be brought up: the saved
			// credentials must take effect now. The boot loop reloads the new
			// config, parks in waiting_for_console with an embedder_unreachable
			// error (cheap retries — the centroid gate skips the console fetch),
			// and self-heals the moment runed comes up. Without this, a
			// reconfigure-while-active would keep the OLD pipelines running and
			// report state "active" for credentials that are not in effect yet.
			s.State.Retrigger()
			result.State = s.State.Current().String()
			result.NextStep = br.Hint
			return result, nil
		}
	}

	bootFrom := time.Now().UTC()
	rr, err := s.ReloadPipelines(ctx)
	if err != nil {
		return nil, fmt.Errorf("configure: bring pipelines online: %w", err)
	}
	result.State = rr.State
	// Console is reachable in BOTH active and waiting_for_bootstrap: the boot
	// loop only reaches those after dialing the console, fetching the manifest,
	// and relaying the centroid set. In waiting_for_bootstrap only runed's model
	// download remains, and the async watcher finishes activation on its own.
	reachable := rr.State == lifecycle.StateActive.String() ||
		rr.State == lifecycle.StateWaitingForBootstrap.String()
	result.ConsoleReachable = &reachable
	switch {
	case rr.State == lifecycle.StateActive.String():
		result.NextStep = "Rune is active. Organizational memory is online."
	case rr.State == lifecycle.StateWaitingForBootstrap.String():
		result.NextStep = waitingForBootstrapHint
	case rr.LastBootError != nil && !rr.LastBootError.At.Before(bootFrom):
		// Boot reached a classified failure with THESE credentials — surface
		// its hint verbatim.
		result.ProbeError = rr.LastBootError.Detail
		result.NextStep = rr.LastBootError.Hint
	case rr.LastBootError != nil:
		// Only an error from BEFORE this configure is available: a prior boot
		// loop is still mid-backoff and has not retried with the new
		// credentials yet (Retrigger is a no-op while a loop is running).
		// Don't blame the fresh setup with the stale error.
		result.NextStep = "Credentials saved; the boot loop retries with them shortly (within ~60s). Run /rune:status to confirm."
	default:
		result.NextStep = "Credentials saved but pipelines are not active yet. Run /rune:status to inspect."
	}

	return result, nil
}

// bootstrapFromRegistration runs the 3-stage connection bootstrap from an
// opaque registration string and returns the resolved Console credentials:
//
//	stage 1 — decode the string, fetch the console CA over an untrusted channel,
//	          verify it against the pinned SHA-256, and persist it.
//	stage 2 — dial with the pinned CA and unwrap the one-time handle → real token.
//	stage 3 — (the caller) write the resolved credentials + bring the pipelines
//	          online via ReloadPipelines (the boot loop dials the console).
func (s *LifecycleService) bootstrapFromRegistration(ctx context.Context, regString string) (*resolvedCreds, error) {
	reg, err := regstr.Decode(regString)
	if err != nil {
		return nil, &domain.RuneError{Code: domain.CodeInvalidInput, Message: "invalid registration string: " + err.Error()}
	}
	if reg.Endpoint == "" {
		return nil, &domain.RuneError{Code: domain.CodeInvalidInput, Message: "registration string has no endpoint"}
	}
	if reg.Token == "" {
		return nil, &domain.RuneError{Code: domain.CodeInvalidInput, Message: "registration string has no wrapping token"}
	}

	// Stage 1: fetch + pin the CA, then persist it before unwrapping. The CA
	// write is fallible; doing it ahead of the irreversible unwrap keeps a
	// disk/permission failure from spending the single-use handle for nothing
	// (a stale CA file is harmless and overwritten on the next attempt).
	caPEM, err := console.FetchCACert(ctx, reg.Endpoint, reg.CASHA256)
	if err != nil {
		// Pre-unwrap: the one-time handle is still unused, so the SAME
		// registration string can be retried once the cause is fixed.
		return nil, &domain.RuneError{
			Code:         domain.CodeConsoleConnection,
			Message:      "bootstrap: fetch console CA: " + err.Error(),
			Retryable:    true,
			RecoveryHint: "The invite was NOT consumed. Fix the connection/endpoint and re-run /rune:configure with the same registration string.",
		}
	}
	caPath, err := config.SaveConsoleCA(caPEM)
	if err != nil {
		// Still pre-unwrap — the handle is unused, same string is reusable.
		return nil, &domain.RuneError{
			Code:         domain.CodeInternal,
			Message:      "bootstrap: save console CA: " + err.Error(),
			Retryable:    true,
			RecoveryHint: "The invite was NOT consumed. Fix ~/.rune permissions/disk space and re-run /rune:configure with the same registration string.",
		}
	}

	// Stage 2: unwrap the one-time handle into the real access token. The
	// handle is spent the moment this returns, so the caller must not fail a
	// later persistence step without surfacing that a fresh invite is needed.
	token, err := console.Unwrap(ctx, reg.Endpoint, caPEM, reg.Token)
	if err != nil {
		return nil, fmt.Errorf("bootstrap: unwrap: %w", err)
	}

	slog.Info("console: bootstrap complete", "endpoint", reg.Endpoint)
	return &resolvedCreds{Endpoint: reg.Endpoint, Token: token, CACertPath: caPath}, nil
}

// ─────────────────────────────────────────────────────────────────────────────
// rune_activate — pre-check + reload
//
//  ActivateStatus:
//	  configure_required  - config.json missing or console block empty
//	  install_pending     - runed socket absent (daemon not installed/running)
//	  active / waiting_for_console / dormant - passed through from reload
// ─────────────────────────────────────────────────────────────────────────────

const (
	ActivateStatusConfigureRequired   = "configure_required"
	ActivateStatusInstallPending      = "install_pending"
	ActivateStatusActive              = "active"
	ActivateStatusWaitingForConsole   = "waiting_for_console"
	ActivateStatusWaitingForBootstrap = "waiting_for_bootstrap"
	ActivateStatusDormant             = "dormant"
)

// Runed reports during STATUS_LOADING
type BootstrapDetail struct {
	Phase      string `json:"phase,omitempty"`       // FETCHING_LLAMA_SERVER / FETCHING_MODEL / STARTING_LLAMA_SERVER
	BytesDone  int64  `json:"bytes_done,omitempty"`  // download progress
	BytesTotal int64  `json:"bytes_total,omitempty"` // 0 when unknown / not downloading
	Message    string `json:"message,omitempty"`     // free-text detail for end-user display
}

// When Status is active / waiting_for_console / dormant, Reload mirrors ReloadPipilines
// When Status is waiting_for_bootstrap, Bootstrap mirrors runed's self-bootstrap progress
type ActivateResult struct {
	OK        bool                   `json:"ok"`
	Status    string                 `json:"status"`
	Hint      string                 `json:"hint,omitempty"`
	Bootstrap *BootstrapDetail       `json:"bootstrap,omitempty"`
	Reload    *ReloadPipelinesResult `json:"reload,omitempty"`
}

const bootstrapProbeTimeout = 2 * time.Second

// daemonReadyTimeout bounds how long /rune:activate waits for the freshly
// spawned runed to open its socket. Shorter than spawn's 15s default so the
// whole activate call stays well under the 30s MCP timeout.
const daemonReadyTimeout = 10 * time.Second

func (s *LifecycleService) Activate(ctx context.Context) (*ActivateResult, error) {
	// Pre-check: config ($HOME/.rune/config.json)
	cfg, err := config.Load()
	if err != nil || cfg == nil {
		return &ActivateResult{
			OK:     true,
			Status: ActivateStatusConfigureRequired,
			Hint:   "Run /rune:configure to write Console credentials.",
		}, nil
	}
	// Configured = an endpoint plus a token source (in-file token, or keyring
	// storage whose entry is validated at boot). A blank Token is normal under
	// keyring storage, so don't treat it as unconfigured.
	hasToken := cfg.Console.Token != "" || cfg.Console.TokenStorage == config.TokenStorageKeyring
	if cfg.Console.Endpoint == "" || !hasToken {
		return &ActivateResult{
			OK:     true,
			Status: ActivateStatusConfigureRequired,
			Hint:   "Console endpoint/token missing in ~/.rune/config.json. Run /rune:configure.",
		}, nil
	}

	// Idempotent: already active → report and stop. Activation is a "come back
	// online" intent; if the pipelines are already up there is nothing to
	// resume, and re-triggering the boot loop would needlessly re-dial the
	// console and re-open the cgo encryptor. (configure itself brings a fresh
	// setup to active, so the /rune:configure → /rune:activate handoff lands
	// here.) A dead session token still surfaces on the next capture/recall or
	// via /rune:status, not by pre-emptively re-booting here.
	if s.State != nil && s.State.Current() == lifecycle.StateActive {
		return &ActivateResult{
			OK:     true,
			Status: ActivateStatusActive,
			Hint:   "Rune is already active. Organizational memory is online.",
		}, nil
	}

	// Idempotent: already waiting on runed's model download — but only when the
	// download is actually in progress (runed answers LOADING). Then keep the
	// watcher alive (restart is a no-op) and report progress; re-running the
	// boot loop would needlessly re-dial the console and re-open the cgo
	// encryptor. Any other answer — DEGRADED, no answer at all (runed crashed
	// mid-download), or OK/IDLE with the watcher gone past its 30min deadline —
	// falls through to the full path below: ensureDaemon respawns a dead
	// daemon and ReloadPipelines re-runs the boot gate to settle the real
	// state. Without the fall-through this branch would keep promising
	// "completes automatically" from a state nothing can complete.
	if s.State != nil && s.State.Current() == lifecycle.StateWaitingForBootstrap {
		if h, ok := s.probeBootstrapHealth(ctx); ok && h.Status == "LOADING" {
			s.startBootstrapWatcher()
			return &ActivateResult{
				OK:        true,
				Status:    ActivateStatusWaitingForBootstrap,
				Hint:      waitingForBootstrapHint,
				Bootstrap: bootstrapDetailFrom(h),
			}, nil
		}
	}

	// Clear a user-initiated deactivation before re-triggering the boot loop.
	//
	// /rune:activate is an explicit "come back online" intent. If the daemon
	// was put to sleep by /rune:deactivate, config.State is "dormant" with
	// reason "user_deactivated" (boot.go also treats a bare dormant state with
	// no reason as user_deactivated). The boot loop reads config.State != active
	// and immediately re-enters dormant, so without clearing the marker here the
	// reload below is a no-op and activation never takes effect.
	//
	// Scope is deliberately limited to user-deactivation: substrate-driven
	// dormancy (not_configured / console_unconfigured) is already handled by the
	// configure_required pre-checks above, so reaching this point means valid
	// credentials exist and the user's own deactivation is the only thing
	// pinning the daemon dormant.
	if cfg.State == "dormant" && (cfg.DormantReason == "" || cfg.DormantReason == "user_deactivated") {
		if err := config.ClearDormant(); err != nil {
			return nil, fmt.Errorf("activate: clear user-deactivated state: %w", err)
		}
	}

	// Pre-check: runed socket path ($RUNE_EMBEDDER_SOCKET or $HOME/.runed/embedding.sock)
	socketPath := embedder.ResolveSocketPath("")
	if socketPath != "" {
		if br := s.ensureDaemon(ctx, socketPath); br != nil {
			return br, nil
		}
	}

	// Bring the pipelines online. The boot loop wires the data plane and pushes
	// the centroid set — both succeed while runed's model is still downloading —
	// then either reaches active (model already loaded, the warm re-activate
	// case) or settles into waiting_for_bootstrap and hands off to the async
	// watcher. Either way this returns within a bounded window; a multi-minute
	// model download never blocks here (the watcher finishes activation).
	rr, err := s.ReloadPipelines(ctx)
	if err != nil {
		return nil, fmt.Errorf("reload pipelines: %w", err)
	}

	// runed still downloading its model: report progress, not a raw "not active".
	// The boot loop already fired the watcher on entering this state.
	if rr.State == lifecycle.StateWaitingForBootstrap.String() {
		return &ActivateResult{
			OK:        true,
			Status:    ActivateStatusWaitingForBootstrap,
			Hint:      waitingForBootstrapHint,
			Bootstrap: s.bootstrapProgress(ctx),
			Reload:    rr,
		}, nil
	}

	return &ActivateResult{
		OK:     rr.OK,
		Status: rr.State,
		Reload: rr,
	}, nil
}

// waitingForBootstrapHint is the user-facing next-step text while runed's model
// downloads. Activation completes on its own via the async bootstrap watcher,
// so it deliberately does NOT ask the user to re-run /rune:activate.
const waitingForBootstrapHint = "runed is bootstrapping (downloading llama-server and/or the embedding model). Activation will complete automatically once the download finishes — no further /rune:activate needed."

// Makes runed reachable at socketPath, spawning if needed
// On failure, install_pending hint tells agent command to run for recovery (spawn.AgentInstallRecoverHint)
func (s *LifecycleService) ensureDaemon(ctx context.Context, socketPath string) *ActivateResult {
	runeBin, err := spawn.ResolveRuneBinary()
	if err != nil {
		return &ActivateResult{
			OK:     true,
			Status: ActivateStatusInstallPending,
			Hint:   fmt.Sprintf("rune CLI not found (%v). Agent recovery: invoke %s, then retry /rune:activate.", err, spawn.AgentInstallRecoveryHint()),
		}
	}

	paths, err := embedderPaths(socketPath)
	if err != nil {
		return &ActivateResult{
			OK:     true,
			Status: ActivateStatusInstallPending,
			Hint:   fmt.Sprintf("could not resolve runed paths: %v", err),
		}
	}

	cfg := spawn.Config{
		RuneBinary:    runeBin,
		SocketPath:    socketPath,
		SpawnLockPath: paths.spawnLock,
		// runed opens its gRPC socket well before its model download starts, so
		// 10s is ample for "reachable" while keeping the whole /rune:activate
		// call comfortably under the 30s MCP timeout. If the socket isn't up by
		// then, return install_pending — a retry is the right recovery.
		ReadyTimeout: daemonReadyTimeout,
	}
	if err := spawn.EnsureDaemon(ctx, cfg); err != nil {
		return &ActivateResult{
			OK:     true,
			Status: ActivateStatusInstallPending,
			Hint:   fmt.Sprintf("auto-spawn of runed via `%s runed --detach` failed: %v. Inspect %s for runed startup logs. Agent recovery: invoke %s, then retry /rune:activate.", runeBin, err, paths.logHint, spawn.AgentInstallRecoveryHint()),
		}
	}
	return nil
}

type runedSpawnPaths struct {
	spawnLock string
	logHint   string
}

func embedderPaths(socketPath string) (runedSpawnPaths, error) {
	dir := filepath.Dir(socketPath)
	if dir == "" || dir == "." {
		return runedSpawnPaths{}, fmt.Errorf("invalid socket path %q", socketPath)
	}
	return runedSpawnPaths{
		spawnLock: filepath.Join(dir, "spawn.lock"),
		logHint:   filepath.Join(dir, "logs", "daemon.log"),
	}, nil
}

// probeBootstrapHealth issues a bounded Health probe against the wired
// embedder. ok=false when no embedder is wired or the probe fails, so callers
// can distinguish "runed answered" from "runed is gone".
func (s *LifecycleService) probeBootstrapHealth(ctx context.Context) (embedder.HealthSnapshot, bool) {
	e := s.Embedder()
	if e == nil {
		return embedder.HealthSnapshot{}, false
	}
	probeCtx, cancel := context.WithTimeout(ctx, bootstrapProbeTimeout)
	defer cancel()
	h, err := e.Health(probeCtx)
	if err != nil {
		return embedder.HealthSnapshot{}, false
	}
	return h, true
}

func bootstrapDetailFrom(h embedder.HealthSnapshot) *BootstrapDetail {
	return &BootstrapDetail{
		Phase:      h.Phase,
		BytesDone:  h.BytesDone,
		BytesTotal: h.BytesTotal,
		Message:    h.Message,
	}
}

// bootstrapProgress returns a snapshot of runed's model-download progress for
// display, or nil if the embedder isn't wired / doesn't answer in time. It does
// NOT start the watcher or decide state — the boot loop owns the model-ready
// gate (bootOnce) and fires the watcher; this is purely a read for the response.
func (s *LifecycleService) bootstrapProgress(ctx context.Context) *BootstrapDetail {
	h, ok := s.probeBootstrapHealth(ctx)
	if !ok {
		return nil
	}
	return bootstrapDetailFrom(h)
}

// StartBootstrapWatcher starts (idempotently) the async goroutine that polls
// runed until its embedding model finishes downloading, then Retriggers the
// boot loop. Exported so cmd/rune-mcp can wire it as the Manager's
// bootstrap-watch callback: the boot loop fires that callback when it settles
// into waiting_for_bootstrap (runed reachable + centroids pushed, model still
// loading). Also called from /rune:activate to revive the watcher if it died.
func (s *LifecycleService) StartBootstrapWatcher() { s.startBootstrapWatcher() }

var bootstrapWatchInterval = 15 * time.Second
var bootstrapWatcherHealthTimeout = 5 * time.Second

const bootstrapWatcherMaxErrors = 3
const bootstrapWatcherDeadline = 30 * time.Minute

func (s *LifecycleService) startBootstrapWatcher() {
	s.bootstrapWatcherMu.Lock()
	if s.bootstrapWatcherRunning { // idempotency
		s.bootstrapWatcherMu.Unlock()
		return
	}
	s.bootstrapWatcherRunning = true
	s.bootstrapWatcherMu.Unlock()

	// Goroutine polls runed until it transition out of STATUS_LOADING,
	// then call State.Retrigger() so boot loop resumes without user interaction
	go s.runBootstrapWatcher()
}

func (s *LifecycleService) runBootstrapWatcher() {
	defer func() {
		s.bootstrapWatcherMu.Lock()
		s.bootstrapWatcherRunning = false
		s.bootstrapWatcherMu.Unlock()
	}()

	ticker := time.NewTicker(bootstrapWatchInterval)
	defer ticker.Stop()

	deadline := time.Now().Add(bootstrapWatcherDeadline)
	consecutiveErrors := 0

	for range ticker.C {
		if time.Now().After(deadline) {
			slog.Warn("bootstrap watcher: total deadline exceeded; operator must re-trigger /rune:activate",
				"deadline", bootstrapWatcherDeadline)
			return
		}

		e := s.Embedder()
		if e == nil { // Embedder is removed
			return
		}

		probeCtx, cancel := context.WithTimeout(context.Background(), bootstrapWatcherHealthTimeout)
		h, err := e.Health(probeCtx)
		cancel()

		if err != nil {
			consecutiveErrors++
			if consecutiveErrors >= bootstrapWatcherMaxErrors {
				slog.Warn("bootstrap watcher: persistent health probe failure; giving up",
					"consecutive_errors", consecutiveErrors, "last_err", err)
				return
			}
			continue
		}
		consecutiveErrors = 0

		switch h.Status {
		case "LOADING": // still bootstrapping
			continue
		case "OK", "IDLE": // bootstrap finished (IDLE = up but idle-suspended, still ready)
			if s.State != nil {
				s.State.Retrigger()
			}
			return
		default:
			// DEGRADED / SHUTTING_DOWN / UNSPECIFIED status need user interaction
			return
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// rune_reload_pipelines (internal — invoked by activate/deactivate, not a tool)
// ─────────────────────────────────────────────────────────────────────────────

// ReloadPipelinesResult.
type ReloadPipelinesResult struct {
	OK    bool   `json:"ok"`
	State string `json:"state"`
	// LastBootError mirrors ConsoleInfo.LastBootError so callers (agent, UI)
	// can fast-fail on this single response — no follow-up diagnostics call
	// needed for the common case of "reload finished, boot failed, here's
	// why". Populated only when state != "active" AND a classified error
	// is available; nil otherwise.
	LastBootError *domain.BootError `json:"last_boot_error,omitempty"`
	Errors        []string          `json:"errors,omitempty"`
}

// ReloadPipelines — re-trigger the boot loop from Dormant.
//
// On a terminal Dormant state (boot loop's goroutine has exited), call
// Manager.Retrigger to spawn a fresh RunBootLoop bound to the same ctx +
// Deps; main.go wires the spawn callback at startup. Manager.Retrigger
// is a silent no-op only while a boot loop is already running (Starting /
// WaitingForConsole); from Active or Dormant it claims the transition via CAS
// and respawns the loop.
//
// /rune:activate from a freshly-spawned MCP server (no ~/.rune/config.json
// at boot, then user ran /rune:configure) reaches Active via this path.
// No process restart is required.
func (s *LifecycleService) ReloadPipelines(ctx context.Context) (*ReloadPipelinesResult, error) {
	// Always re-trigger so config changes such as new console endpoint and rotated token are picked
	// without restarting MCP
	s.State.Retrigger()
	s.waitForBootProgress(ctx, 5*time.Second)

	result := &ReloadPipelinesResult{
		OK:    true,
		State: s.State.Current().String(),
	}

	if s.State.Current() != lifecycle.StateActive {
		// Boot did not reach active within the 5s wait window. Surface the
		// most recent classified boot error so the caller can fast-fail
		// without needing a separate diagnostics call. May still be nil
		// (e.g., boot loop is genuinely in-flight and hasn't recorded an
		// error yet) — in that case the agent should follow up with
		// diagnostics per the Fast-Fail Rule.
		if be := s.State.LastBootError(); be != nil {
			result.LastBootError = be
		}
	}

	return result, nil
}

// waitForBootProgress polls Manager.Current() until either Active or a
// terminal Dormant is reached, or the deadline elapses. The caller has
// already triggered a fresh boot loop; this just gives it room to make
// progress before we snapshot state for the response. WaitingForConsole
// (transient retry) is treated as still-in-progress because the loop is
// actively retrying with backoff and may yet reach Active.
//
// Initial 150ms grace period: Retrigger schedules a `go RunBootLoop(...)`
// — there is a brief window between the call returning and the spawned
// goroutine reaching its first `m.SetState(StateStarting)`. Without the
// grace, the very first state read can still see the prior Dormant
// snapshot and exit immediately.
func (s *LifecycleService) waitForBootProgress(ctx context.Context, timeout time.Duration) {
	deadline := time.Now().Add(timeout)

	select {
	case <-ctx.Done():
		return
	case <-time.After(150 * time.Millisecond):
	}

	for time.Now().Before(deadline) {
		switch s.State.Current() {
		case lifecycle.StateActive, lifecycle.StateDormant, lifecycle.StateWaitingForBootstrap:
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// rune_deactivate — flip active -> dormant, preserving credentials.
// ─────────────────────────────────────────────────────────────────────────────

// DeactivateResult.
//
// AlreadyDormant distinguishes a no-op (Rune was already dormant) from a fresh
// active→dormant transition, so the caller can say "already deactivated"
// instead of "now dormant". Hint carries the user-facing next step.
type DeactivateResult struct {
	OK             bool   `json:"ok"`
	State          string `json:"state"`
	AlreadyDormant bool   `json:"already_dormant,omitempty"`
	Hint           string `json:"hint,omitempty"`
}

// Deactivate is the inverse of Activate: it flips the running pipelines to
// dormant without touching Console credentials, so /rune:activate can resume
// from the same config. MarkDormant persists state=dormant (reason
// user_deactivated); Retrigger then re-runs the boot loop, which reads the
// dormant config and settles the Manager into StateDormant -- at which point
// the state gate rejects capture/recall with PIPELINE_NOT_READY.
func (s *LifecycleService) Deactivate(ctx context.Context) (*DeactivateResult, error) {
	// Idempotent: already dormant → report, don't re-mark or re-run the boot
	// loop. capture/recall are already gated in this state, so there is nothing
	// to pause.
	if s.State != nil && s.State.Current() == lifecycle.StateDormant {
		return &DeactivateResult{
			OK:             true,
			State:          lifecycle.StateDormant.String(),
			AlreadyDormant: true,
			Hint:           "Rune is already dormant. Organizational memory is paused — /rune:activate to resume.",
		}, nil
	}
	if err := config.MarkDormant("user_deactivated"); err != nil {
		return nil, fmt.Errorf("deactivate: mark dormant: %w", err)
	}
	s.State.Retrigger()
	s.waitForBootProgress(ctx, 5*time.Second)
	return &DeactivateResult{
		OK:    true,
		State: s.State.Current().String(),
		Hint:  "Rune is now dormant. Organizational memory is paused — /rune:activate to resume.",
	}, nil
}
