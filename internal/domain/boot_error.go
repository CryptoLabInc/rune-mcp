package domain

// BootError — structured failure surfaced by the boot loop, exposed via
// diagnostics so agents (and humans) can fast-fail with a specific kind +
// hint instead of probing the system manually.
//
// The boot loop otherwise logs errors and stores a free-form string in
// lifecycle.Manager.LastError(); callers need a stable enum to branch on.
//
// Leaf type: imports stdlib only. Classifier lives in internal/lifecycle.

import "time"

// BootErrorKind — stable enum string for agent/UI branching.
// Add new values only at the end to preserve schema compatibility.
type BootErrorKind string

const (
	// Catch-all for unrecognized failures. Detail contains the raw message;
	// the agent should ask the user to share it with an admin.
	BootErrUnknown BootErrorKind = "unknown"

	// ── Config-side (terminal Dormant) ───────────────────────────────
	BootErrConfigMissing        BootErrorKind = "config_missing"         // ~/.rune/config.json absent
	BootErrConfigInvalid        BootErrorKind = "config_invalid"         // state=unknown / parse fail
	BootErrConfigParse          BootErrorKind = "config_parse"           // JSON parse fail
	BootErrUserDeactivated      BootErrorKind = "user_deactivated"       // state=dormant by /rune:deactivate
	BootErrConsoleNotConfigured BootErrorKind = "console_not_configured" // endpoint/token empty

	// ── Console dial / NewClient (sync, before any RPC) ────────────────
	BootErrConsoleBadEndpoint BootErrorKind = "console_bad_endpoint" // ParseEndpoint failed
	BootErrConsoleCAFile      BootErrorKind = "console_ca_file"      // CA cert path unreadable / not PEM
	BootErrConsoleDialOpts    BootErrorKind = "console_dial_opts"    // grpc.NewClient rejected options

	// ── Console RPC (GetAgentManifest path) ────────────────────────────
	BootErrConsoleTLSHandshake BootErrorKind = "console_tls_handshake" // x509: signed by unknown authority / expired / etc.
	BootErrConsoleTLSHostname  BootErrorKind = "console_tls_hostname"  // cert SAN does not match endpoint host
	BootErrConsoleDNS          BootErrorKind = "console_dns"           // hostname resolution failed
	BootErrConsoleNetwork      BootErrorKind = "console_network"       // TCP unreachable / refused / reset
	BootErrConsoleTimeout      BootErrorKind = "console_timeout"       // gRPC DeadlineExceeded
	BootErrConsoleAuth         BootErrorKind = "console_auth"          // gRPC Unauthenticated (bad token)
	BootErrConsolePermission   BootErrorKind = "console_permission"    // gRPC PermissionDenied (role lacks scope)
	BootErrConsoleRateLimit    BootErrorKind = "console_rate_limit"    // gRPC ResourceExhausted
	BootErrConsoleInvalidInput BootErrorKind = "console_invalid_input" // gRPC InvalidArgument
	BootErrConsoleManifest     BootErrorKind = "console_manifest"      // Console responded but manifest empty/invalid/unparseable
	BootErrConsoleInternal     BootErrorKind = "console_internal"      // gRPC Internal / other server-side

	// ── Post-Console adapters ──────────────────────────────────────────
	BootErrEmbedderUnreachable BootErrorKind = "embedder_unreachable" // UDS socket missing / runed down
	BootErrRunespaceInit       BootErrorKind = "runespace_init"       // client-side runespace encryptor (runespacecrypto.Open) failed
	BootErrRunespaceIndex      BootErrorKind = "runespace_index"      // runespace index unavailable (reached via the console, not mcp)
	BootErrKeySave             BootErrorKind = "key_save"             // SaveEncKey / KeyDir filesystem failure
	BootErrLocalIO             BootErrorKind = "local_io"             // generic local FS / permissions
)

// BootPhase — which step of the boot sequence produced the error.
// Useful for distinguishing same-kind errors at different phases.
type BootPhase string

const (
	BootPhaseConfigLoad      BootPhase = "config_load"
	BootPhaseConfigCheck     BootPhase = "config_check"
	BootPhaseConsoleDial     BootPhase = "console_dial"
	BootPhaseConsoleManifest BootPhase = "console_manifest"
	BootPhaseKeySave         BootPhase = "key_save"
	BootPhaseEmbedderDial    BootPhase = "embedder_dial"
	BootPhaseRunespaceInit   BootPhase = "runespace_init"
	BootPhaseRunespaceIndex  BootPhase = "runespace_index"
)

// BootError — surfaced via diagnostics.console.last_boot_error.
//
// JSON shape (stable contract for agents):
//
//	{
//	  "kind":     "console_tls_handshake",
//	  "detail":   "rpc error: code = Unavailable desc = ... x509: ...",
//	  "hint":     "CA cert at /Users/.../ca.pem does not verify server cert from tcp://X. Re-fetch the current CA from your Console admin.",
//	  "phase":    "console_manifest",
//	  "at":       "2026-05-16T10:09:23Z",
//	  "attempts": 4
//	}
//
// Hint is interpolated with concrete values (endpoint, ca path) so the agent
// can relay it to the user verbatim. Detail is raw text for human debugging
// and the unknown-kind fallback path.
type BootError struct {
	Kind     BootErrorKind `json:"kind"`
	Detail   string        `json:"detail"`
	Hint     string        `json:"hint,omitempty"`
	Phase    BootPhase     `json:"phase,omitempty"`
	At       time.Time     `json:"at"`
	Attempts int           `json:"attempts,omitempty"`
}

// Retryable — true when blind retry (no user action) has a reasonable chance
// of succeeding. False when the user must change something first (re-issue
// token, fix CA cert, edit config, etc.).
//
// The boot loop itself may still retry on bootRetry results regardless of
// this flag — Retryable is for the agent / UI to decide whether to suggest
// "wait + recheck" vs "fix the underlying issue before recheck."
func (e *BootError) Retryable() bool {
	if e == nil {
		return false
	}
	switch e.Kind {
	// Won't self-heal without user action.
	case BootErrConfigMissing,
		BootErrConfigInvalid,
		BootErrConfigParse,
		BootErrUserDeactivated,
		BootErrConsoleNotConfigured,
		BootErrConsoleBadEndpoint,
		BootErrConsoleCAFile,
		BootErrConsoleDialOpts,
		BootErrConsoleTLSHandshake,
		BootErrConsoleTLSHostname,
		BootErrConsoleAuth,
		BootErrConsolePermission,
		BootErrConsoleInvalidInput,
		BootErrConsoleManifest:
		return false
	default:
		// Transient: network blips, DNS, timeouts, rate limits, daemon down
		// while restarting, etc.
		return true
	}
}
