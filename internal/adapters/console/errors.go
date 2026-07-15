package console

import (
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Error — console adapter's typed error. Wraps a cause (gRPC error or IO error).
// Service layer catches these and converts to domain.RuneError for MCP responses.
type Error struct {
	Code      string
	Message   string
	Retryable bool
	Cause     error
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

// Unwrap allows errors.Is / errors.As to inspect the cause.
func (e *Error) Unwrap() error { return e.Cause }

// The first five mirror Python rune-console categories. The next three were
// added after auditing rune-console/internal/server/grpc.go: the server
// actually returns codes.PermissionDenied, codes.InvalidArgument, and
// codes.ResourceExhausted as part of role / input / rate-limit handling, and
// these need distinct sentinels so callers can tell them apart from generic
// CONSOLE_INTERNAL (and avoid retry-storming on permanent failures like a
// role that lacks a scope).
var (
	ErrConsoleUnavailable      = &Error{Code: "CONSOLE_UNAVAILABLE", Retryable: true}
	ErrConsoleAuthFailed       = &Error{Code: "CONSOLE_AUTH_FAILED", Retryable: false}
	ErrConsoleKeyNotFound      = &Error{Code: "CONSOLE_KEY_NOT_FOUND", Retryable: false}
	ErrConsoleInternal         = &Error{Code: "CONSOLE_INTERNAL", Retryable: true}
	ErrConsoleTimeout          = &Error{Code: "CONSOLE_TIMEOUT", Retryable: true}
	ErrConsolePermissionDenied = &Error{Code: "CONSOLE_PERMISSION_DENIED", Retryable: false}
	ErrConsoleInvalidInput     = &Error{Code: "CONSOLE_INVALID_INPUT", Retryable: false}
	ErrConsoleTopKExceeded     = &Error{Code: "CONSOLE_TOPK_EXCEEDED", Retryable: false}
	ErrConsoleRateLimited      = &Error{Code: "CONSOLE_RATE_LIMITED", Retryable: true}
	// ErrConsoleWrongCentroidVersion — the runespace engine replaced its centroid
	// set and the insert was routed against the stale one (§9.2 C3). Not
	// retryable as-is: resync centroids (console relay → runed), re-route, and
	// retry once with the same id. Wire contract: FailedPrecondition with the
	// "WRONG_CENTROID_VERSION" message prefix from the console Insert handler.
	ErrConsoleWrongCentroidVersion = &Error{Code: "WRONG_CENTROID_VERSION", Retryable: false}

	// ErrNotHTTPScheme — returned by HealthFallback when endpoint is not http(s).
	ErrNotHTTPScheme = errors.New("console: endpoint not http(s) scheme")
)

// MapGRPCError maps a gRPC status error to the appropriate console sentinel + cause.
//
// Mappings cover both server-emitted codes (rune-console/internal/server/
// grpc.go) and transport-layer codes (gRPC runtime / client deadline):
//
//	Unauthenticated     → ErrConsoleAuthFailed       (token validation)
//	PermissionDenied    → ErrConsolePermissionDenied (role scope check)
//	InvalidArgument     → ErrConsoleTopKExceeded     (msg contains "exceeds limit": top_k over role limit)
//	InvalidArgument     → ErrConsoleInvalidInput     (any other bad client input)
//	ResourceExhausted   → ErrConsoleRateLimited      (token rate limit)
//	FailedPrecondition  → ErrConsoleWrongCentroidVersion (msg prefix "WRONG_CENTROID_VERSION": stale centroid routing)
//	NotFound            → ErrConsoleKeyNotFound      (server doesn't emit this today; mapped for future)
//	Unavailable         → ErrConsoleUnavailable      (transport: network / server down)
//	DeadlineExceeded    → ErrConsoleTimeout          (transport: client deadline)
//	Internal / <other>  → ErrConsoleInternal         (server failures + non-gRPC fallback)
//
// Returns nil for nil input.
func MapGRPCError(err error) error {
	if err == nil {
		return nil
	}

	st, ok := status.FromError(err)
	if !ok {
		return &Error{
			Code:      ErrConsoleInternal.Code,
			Message:   err.Error(),
			Retryable: true,
			Cause:     err,
		}
	}

	switch st.Code() {
	case codes.Unauthenticated:
		return &Error{
			Code:      ErrConsoleAuthFailed.Code,
			Message:   st.Message(),
			Retryable: false,
			Cause:     err,
		}
	case codes.PermissionDenied:
		return &Error{
			Code:      ErrConsolePermissionDenied.Code,
			Message:   st.Message(),
			Retryable: false,
			Cause:     err,
		}
	case codes.InvalidArgument:
		// The console server returns codes.InvalidArgument both for genuinely
		// malformed input (base64 deserialization, oversized lists) and for a
		// top_k that exceeds the token role's limit. The latter carries the
		// message "top_k N exceeds limit M for role 'X'" (rune-console
		// runeconsole/internal/tokens/errors.go ErrTopKExceeded) — match both stable
		// substrings ("top_k" + "exceeds limit") so callers can surface a
		// dedicated TOPK_LIMIT error instead of a generic invalid-input one,
		// while staying robust against unrelated "exceeds ..." messages.
		if msg := st.Message(); strings.Contains(msg, "top_k") && strings.Contains(msg, "exceeds limit") {
			return &Error{
				Code:      ErrConsoleTopKExceeded.Code,
				Message:   st.Message(),
				Retryable: false,
				Cause:     err,
			}
		}
		return &Error{
			Code:      ErrConsoleInvalidInput.Code,
			Message:   st.Message(),
			Retryable: false,
			Cause:     err,
		}
	case codes.ResourceExhausted:
		return &Error{
			Code:      ErrConsoleRateLimited.Code,
			Message:   st.Message(),
			Retryable: true,
			Cause:     err,
		}
	case codes.FailedPrecondition:
		// The console emits FailedPrecondition only for the centroid-version
		// mismatch relay (Insert handler, "WRONG_CENTROID_VERSION: ..."). Match
		// the prefix so an unrelated future FailedPrecondition still falls
		// through to CONSOLE_INTERNAL instead of triggering a centroid resync.
		if strings.HasPrefix(st.Message(), "WRONG_CENTROID_VERSION") {
			return &Error{
				Code:      ErrConsoleWrongCentroidVersion.Code,
				Message:   st.Message(),
				Retryable: false,
				Cause:     err,
			}
		}
		return &Error{
			Code:      ErrConsoleInternal.Code,
			Message:   st.Message(),
			Retryable: false,
			Cause:     err,
		}
	case codes.NotFound:
		return &Error{
			Code:      ErrConsoleKeyNotFound.Code,
			Message:   st.Message(),
			Retryable: false,
			Cause:     err,
		}
	case codes.Unavailable:
		return &Error{
			Code:      ErrConsoleUnavailable.Code,
			Message:   st.Message(),
			Retryable: true,
			Cause:     err,
		}
	case codes.DeadlineExceeded:
		return &Error{
			Code:      ErrConsoleTimeout.Code,
			Message:   st.Message(),
			Retryable: true,
			Cause:     err,
		}
	default:
		return &Error{
			Code:      ErrConsoleInternal.Code,
			Message:   st.Message(),
			Retryable: true,
			Cause:     err,
		}
	}
}
