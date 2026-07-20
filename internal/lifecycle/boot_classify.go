package lifecycle

// boot_classify.go — translate raw boot errors into structured domain.BootError
// so diagnostics (and downstream agents) can branch on a stable enum instead
// of pattern-matching free-form strings.
//
// Each error site in boot.go calls ClassifyBootError(err, ctx) with the phase
// it was in. The classifier walks the wrap chain via errors.As, checks
// known sentinels (console.Error, net.OpError, x509.UnknownAuthorityError,
// gRPC status), and falls through to a phase-specific fallback so every
// failure produces a non-empty Kind + Hint.

import (
	"crypto/x509"
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/url"
	"strings"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/domain"
)

// BootErrCtx — context the boot loop hands to the classifier so hints can
// be interpolated with concrete values (endpoint, ca path) the user can act on.
type BootErrCtx struct {
	Phase           domain.BootPhase
	ConsoleEndpoint string
	ConsoleCAPath   string
	Attempts        int
}

// ClassifyBootError maps any error from the boot sequence to a BootError.
// Returns nil if err is nil. Always returns a non-nil BootError otherwise,
// with Kind defaulting to BootErrUnknown only when nothing matched.
func ClassifyBootError(err error, c BootErrCtx) *domain.BootError {
	if err == nil {
		return nil
	}

	be := &domain.BootError{
		Kind:     domain.BootErrUnknown,
		Detail:   err.Error(),
		Phase:    c.Phase,
		At:       time.Now().UTC(),
		Attempts: c.Attempts,
	}

	// The embedder phase talks to the LOCAL runed daemon over a UDS, not the
	// Console. The net/gRPC classifiers below are Console-oriented and
	// phase-blind, so without this guard a transport failure from runed
	// (gRPC Unavailable / net.OpError) is mislabeled console_network — "Console
	// not reachable" — even though the Console already answered GetAgentManifest
	// earlier in the same boot. Route embedder-phase errors to their own
	// classifier first.
	if c.Phase == domain.BootPhaseEmbedderDial {
		classifyEmbedderErr(err, be)
		return be
	}

	// Order matters: more specific checks first. Each branch fills Kind+Hint
	// and returns. The phase-aware fallback at the bottom catches anything
	// that didn't match a structured sentinel.

	if classifyConfigErr(err, be, c) {
		return be
	}
	if classifyTLSErr(err, be, c) {
		return be
	}
	if classifyConsoleSentinel(err, be, c) {
		return be
	}
	if classifyNetErr(err, be, c) {
		return be
	}
	if classifyGRPCStatus(err, be, c) {
		return be
	}

	// Last resort: phase-aware fallback.
	applyPhaseFallback(be, c)
	return be
}

// ClassifyDormantReason maps a Dormant return's reason string to a BootError.
// These are terminal — the boot loop has stopped retrying. The Hint tells the
// user what action will move out of dormant.
func ClassifyDormantReason(reason string) *domain.BootError {
	be := &domain.BootError{
		Detail: "dormant: " + reason,
		At:     time.Now().UTC(),
	}
	switch reason {
	case "not_configured":
		be.Kind = domain.BootErrConfigMissing
		be.Hint = "~/.rune/config.json is missing. Run /rune:configure to set up Console credentials."
	case "console_unconfigured":
		be.Kind = domain.BootErrConsoleNotConfigured
		be.Hint = "Console endpoint or token is empty in ~/.rune/config.json. Run /rune:configure."
	case "user_deactivated":
		be.Kind = domain.BootErrUserDeactivated
		be.Hint = "Rune is dormant by user choice. Run /rune:activate to resume."
	case "invalid_state":
		be.Kind = domain.BootErrConfigInvalid
		be.Hint = "config.json has an unknown state value. Edit it or re-run /rune:configure."
	case "":
		be.Kind = domain.BootErrConfigInvalid
		be.Hint = "Dormant for unknown reason. Run /rune:configure to reset."
	default:
		be.Kind = domain.BootErrConfigInvalid
		be.Hint = "Dormant: " + reason
	}
	return be
}

// embedderUnreachableHint is the recovery text for a runed daemon that can't be
// reached on its local socket. /rune:activate spawns runed automatically, so a
// persistent failure means the daemon isn't installed, crashed, or never came
// up — point the user at a retry and the daemon log.
const embedderUnreachableHint = "The runed embedding daemon is not reachable on its local socket. Run /rune:activate to (re)start it; if this persists, inspect ~/.runed/logs/daemon.log."

// classifyEmbedderErr classifies a failure from the runed (embedder) phase.
// Every failure here is runed-local — the daemon is unreachable, still
// starting, or rejecting the request — never a Console problem. A gRPC
// FailedPrecondition means runed is up but not ready (e.g. mid model-download),
// which retries into readiness; everything else is treated as unreachable.
func classifyEmbedderErr(err error, be *domain.BootError) {
	be.Kind = domain.BootErrEmbedderUnreachable
	if st, ok := status.FromError(err); ok && st.Code() == codes.FailedPrecondition {
		be.Hint = "The runed daemon is running but not ready yet — it may still be downloading its embedding model. Boot retries automatically; check progress with /rune:status."
		return
	}
	be.Hint = embedderUnreachableHint
}

// ── individual classifiers ────────────────────────────────────────────────

// classifyConfigErr — fs.ErrNotExist, JSON parse errors at config_load phase.
func classifyConfigErr(err error, be *domain.BootError, c BootErrCtx) bool {
	if errors.Is(err, fs.ErrNotExist) && c.Phase == domain.BootPhaseConfigLoad {
		be.Kind = domain.BootErrConfigMissing
		be.Hint = "~/.rune/config.json not found. Run /rune:configure to set up."
		return true
	}
	// JSON parse during config load
	var syntaxErr *strings.Reader // placeholder; real check below
	_ = syntaxErr
	if c.Phase == domain.BootPhaseConfigLoad && strings.Contains(strings.ToLower(err.Error()), "unmarshal") {
		be.Kind = domain.BootErrConfigParse
		be.Hint = "~/.rune/config.json is not valid JSON. Edit or re-run /rune:configure."
		return true
	}
	return false
}

// classifyTLSErr — x509 typed errors + TLS message patterns (for cases where
// gRPC wraps the typed error in plain string).
func classifyTLSErr(err error, be *domain.BootError, c BootErrCtx) bool {
	// 1) CA cert file load failure — surfaces as fs.PathError wrapped by console.NewClient.
	var pathErr *fs.PathError
	if errors.As(err, &pathErr) && strings.Contains(strings.ToLower(err.Error()), "ca cert") {
		be.Kind = domain.BootErrConsoleCAFile
		be.Hint = fmt.Sprintf("CA cert file %q could not be opened. Check the path in ~/.rune/config.json.", c.ConsoleCAPath)
		return true
	}
	// 2) Typed x509 errors (may be wrapped by gRPC).
	var unknownAuth x509.UnknownAuthorityError
	if errors.As(err, &unknownAuth) {
		be.Kind = domain.BootErrConsoleTLSHandshake
		be.Hint = tlsCAMismatchHint(c)
		return true
	}
	var hostnameErr x509.HostnameError
	if errors.As(err, &hostnameErr) {
		be.Kind = domain.BootErrConsoleTLSHostname
		be.Hint = fmt.Sprintf("Console server cert does not include %q in its SAN. Use an endpoint that matches the cert, or re-issue the cert with the right SAN.", c.ConsoleEndpoint)
		return true
	}
	var certInvalid x509.CertificateInvalidError
	if errors.As(err, &certInvalid) {
		be.Kind = domain.BootErrConsoleTLSHandshake
		be.Hint = "Console server cert is not valid (expired, not yet valid, or malformed). Re-issue the cert."
		return true
	}

	// 3) String fallback — gRPC sometimes wraps x509 errors as plain strings
	// without preserving the typed error. Catch the common phrases.
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "tls:") || strings.Contains(msg, "x509:") ||
		strings.Contains(msg, "authentication handshake failed") {
		switch {
		case strings.Contains(msg, "signed by unknown authority"),
			strings.Contains(msg, "unable to verify"),
			strings.Contains(msg, "unknown ca"):
			be.Kind = domain.BootErrConsoleTLSHandshake
			be.Hint = tlsCAMismatchHint(c)
			return true
		case strings.Contains(msg, "is valid for"),
			strings.Contains(msg, "not valid for"),
			strings.Contains(msg, "doesn't contain any ip sans"),
			strings.Contains(msg, "san"):
			be.Kind = domain.BootErrConsoleTLSHostname
			be.Hint = fmt.Sprintf("Console server cert does not match the endpoint %q. Either use a different endpoint or re-issue the cert with the right SAN.", c.ConsoleEndpoint)
			return true
		case strings.Contains(msg, "expired"),
			strings.Contains(msg, "not yet valid"):
			be.Kind = domain.BootErrConsoleTLSHandshake
			be.Hint = "Console server cert is expired or not yet valid. Re-issue the cert."
			return true
		default:
			be.Kind = domain.BootErrConsoleTLSHandshake
			be.Hint = "TLS handshake with Console failed. Detail field contains specifics — share with your Console admin."
			return true
		}
	}
	return false
}

// classifyConsoleSentinel — console.Error (already categorized by MapGRPCError).
func classifyConsoleSentinel(err error, be *domain.BootError, c BootErrCtx) bool {
	var ve *console.Error
	if !errors.As(err, &ve) {
		return false
	}
	switch ve.Code {
	case console.ErrConsoleAuthFailed.Code:
		be.Kind = domain.BootErrConsoleAuth
		// The session token is derived from a one-time invite (configure →
		// Unwrap); there is no self-service token reissue. An Unauthenticated
		// here means the token expired or was revoked, and the only recovery is
		// a fresh invite. (If it self-heals on the next retry, it was a transient
		// console blip rather than a dead token.)
		be.Hint = "Console rejected the token — it may have expired or been revoked. There is no manual token reissue: request a new Rune invite email and re-run /rune:configure with the fresh registration string."
	case console.ErrConsolePermissionDenied.Code:
		be.Kind = domain.BootErrConsolePermission
		be.Hint = "Token authenticated but lacks the required role/scope. Re-issue the token with the correct role."
	case console.ErrConsoleRateLimited.Code:
		be.Kind = domain.BootErrConsoleRateLimit
		be.Hint = "Console rate-limited this token. Wait and retry, or check token quota."
	case console.ErrConsoleTimeout.Code:
		be.Kind = domain.BootErrConsoleTimeout
		be.Hint = fmt.Sprintf("Console did not respond within the timeout (endpoint %s). Network latency or server overload.", c.ConsoleEndpoint)
	case console.ErrConsoleUnavailable.Code:
		// Could be TLS or pure network — classifyTLSErr ran first, so if we
		// got here it's most likely network. But fall through to a network
		// hint that's still helpful if it WAS TLS.
		be.Kind = domain.BootErrConsoleNetwork
		be.Hint = fmt.Sprintf("Console endpoint %s is not reachable. Verify TCP connectivity (e.g., `nc -vz host port`) and firewall, and confirm the server is running.", c.ConsoleEndpoint)
	case console.ErrConsoleKeyNotFound.Code:
		be.Kind = domain.BootErrConsoleManifest
		be.Hint = "Console returned NotFound for this token's manifest. Confirm the token is provisioned for an active agent."
	case console.ErrConsoleInvalidInput.Code:
		be.Kind = domain.BootErrConsoleInvalidInput
		be.Hint = "Console rejected the request as invalid input. Likely a malformed token or endpoint."
	case console.ErrConsoleInternal.Code:
		be.Kind = domain.BootErrConsoleInternal
		be.Hint = "Console server returned an internal error. Share the detail field with your Console admin."
	default:
		be.Kind = domain.BootErrConsoleInternal
		be.Hint = "Console returned an unrecognized error."
	}
	return true
}

// classifyNetErr — net.DNSError, net.OpError for non-gRPC-wrapped failures.
func classifyNetErr(err error, be *domain.BootError, c BootErrCtx) bool {
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		be.Kind = domain.BootErrConsoleDNS
		be.Hint = fmt.Sprintf("Cannot resolve hostname for %s: %s. Check the endpoint spelling and DNS.", c.ConsoleEndpoint, dnsErr.Err)
		return true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		be.Kind = domain.BootErrConsoleNetwork
		be.Hint = fmt.Sprintf("Network error contacting %s during %s: %s.", c.ConsoleEndpoint, opErr.Op, opErr.Err)
		return true
	}
	// URL parse errors during ParseEndpoint
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		be.Kind = domain.BootErrConsoleBadEndpoint
		be.Hint = fmt.Sprintf("Endpoint %q could not be parsed: %s. Format: host:port or tcp://host:port.", c.ConsoleEndpoint, urlErr.Err)
		return true
	}
	return false
}

// classifyGRPCStatus — bare gRPC status (when not wrapped in console.Error).
func classifyGRPCStatus(err error, be *domain.BootError, c BootErrCtx) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch st.Code() {
	case codes.Unauthenticated:
		be.Kind = domain.BootErrConsoleAuth
		be.Hint = "Console rejected the token as unauthenticated — it may have expired or been revoked. Request a new Rune invite and re-run /rune:configure with the fresh registration string."
	case codes.PermissionDenied:
		be.Kind = domain.BootErrConsolePermission
		be.Hint = "Token authenticated but lacks required role (gRPC PermissionDenied)."
	case codes.ResourceExhausted:
		be.Kind = domain.BootErrConsoleRateLimit
		be.Hint = "Console rate-limited this token (gRPC ResourceExhausted)."
	case codes.Unavailable:
		be.Kind = domain.BootErrConsoleNetwork
		be.Hint = fmt.Sprintf("Console endpoint %s is not reachable (gRPC Unavailable).", c.ConsoleEndpoint)
	case codes.DeadlineExceeded:
		be.Kind = domain.BootErrConsoleTimeout
		be.Hint = "Console did not respond within the deadline."
	case codes.NotFound:
		be.Kind = domain.BootErrConsoleManifest
		be.Hint = "Console returned NotFound — manifest may not exist for this token."
	case codes.InvalidArgument:
		be.Kind = domain.BootErrConsoleInvalidInput
		be.Hint = "Console rejected the request as invalid input."
	case codes.OK:
		return false // not actually an error
	default:
		be.Kind = domain.BootErrConsoleInternal
		be.Hint = fmt.Sprintf("Console returned gRPC %s.", st.Code())
	}
	return true
}

// applyPhaseFallback — when no structured classifier matched, infer kind+hint
// from which boot phase we were in. Guarantees every BootError has a useful
// hint even for unknown wrappers.
func applyPhaseFallback(be *domain.BootError, c BootErrCtx) {
	if be.Hint != "" && be.Kind != domain.BootErrUnknown {
		return
	}
	switch c.Phase {
	case domain.BootPhaseConfigLoad:
		be.Kind = domain.BootErrConfigInvalid
		be.Hint = "Failed to load ~/.rune/config.json. Check the file is valid JSON with the expected schema."
	case domain.BootPhaseConfigCheck:
		be.Kind = domain.BootErrConfigInvalid
		be.Hint = "Config loaded but has invalid values."
	case domain.BootPhaseConsoleDial:
		be.Kind = domain.BootErrConsoleDialOpts
		be.Hint = "Could not initialize the Console gRPC client (endpoint, CA, or dial options rejected)."
	case domain.BootPhaseConsoleManifest:
		be.Kind = domain.BootErrConsoleManifest
		be.Hint = "Console responded but the agent manifest could not be parsed or was empty."
	case domain.BootPhaseKeySave:
		be.Kind = domain.BootErrKeySave
		be.Hint = "Could not save key material to ~/.rune/. Check filesystem permissions."
	case domain.BootPhaseEmbedderDial:
		be.Kind = domain.BootErrEmbedderUnreachable
		be.Hint = embedderUnreachableHint
	case domain.BootPhaseRunespaceInit:
		be.Kind = domain.BootErrRunespaceInit
		be.Hint = "The client-side runespace encryptor could not be initialized — the EncKey from the console manifest may be invalid. Re-run /rune:configure."
	case domain.BootPhaseRunespaceIndex:
		be.Kind = domain.BootErrRunespaceIndex
		be.Hint = "The runespace index is unavailable. mcp reaches it via the console — check console→runespace connectivity with your Console admin."
	default:
		be.Kind = domain.BootErrUnknown
		be.Hint = "Unrecognized boot failure. Detail field has the raw error — share with your Console admin."
	}
}

// tlsCAMismatchHint — interpolated CA-mismatch hint used both by typed x509
// detection and string fallback so the wording stays consistent.
func tlsCAMismatchHint(c BootErrCtx) string {
	caPath := c.ConsoleCAPath
	if caPath == "" {
		caPath = "(no CA cert configured)"
	}
	endpoint := c.ConsoleEndpoint
	if endpoint == "" {
		endpoint = "the Console server"
	}
	return fmt.Sprintf(
		"Console server's certificate is not signed by the CA at %s. "+
			"The CA may have been regenerated on the server side (same CN, different keypair) "+
			"and the local CA is stale. Re-fetch the current CA from %s admin and replace ~/.rune/certs/ca.pem.",
		caPath, endpoint,
	)
}
