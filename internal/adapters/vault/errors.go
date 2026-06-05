package vault

import (
	"errors"
	"strings"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Error — vault adapter's typed error. Wraps a cause (gRPC error or IO error).
// Service layer catches these and converts to domain.RuneError for MCP responses.
// Spec: docs/v04/spec/components/vault.md §에러 분류.
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

// Sentinel errors — vault.md §에러 분류.
//
// The first five mirror Python rune-vault categories. The next three were
// added after auditing rune-admin/vault/internal/server/grpc.go: the server
// actually returns codes.PermissionDenied, codes.InvalidArgument, and
// codes.ResourceExhausted as part of role / input / rate-limit handling, and
// these need distinct sentinels so callers can tell them apart from generic
// VAULT_INTERNAL (and avoid retry-storming on permanent failures like a
// role that lacks a scope).
var (
	ErrVaultUnavailable      = &Error{Code: "VAULT_UNAVAILABLE", Retryable: true}
	ErrVaultAuthFailed       = &Error{Code: "VAULT_AUTH_FAILED", Retryable: false}
	ErrVaultKeyNotFound      = &Error{Code: "VAULT_KEY_NOT_FOUND", Retryable: false}
	ErrVaultInternal         = &Error{Code: "VAULT_INTERNAL", Retryable: true}
	ErrVaultTimeout          = &Error{Code: "VAULT_TIMEOUT", Retryable: true}
	ErrVaultPermissionDenied = &Error{Code: "VAULT_PERMISSION_DENIED", Retryable: false}
	ErrVaultInvalidInput     = &Error{Code: "VAULT_INVALID_INPUT", Retryable: false}
	ErrVaultTopKExceeded     = &Error{Code: "VAULT_TOPK_EXCEEDED", Retryable: false}
	ErrVaultRateLimited      = &Error{Code: "VAULT_RATE_LIMITED", Retryable: true}

	// ErrNotHTTPScheme — returned by HealthFallback when endpoint is not http(s).
	ErrNotHTTPScheme = errors.New("vault: endpoint not http(s) scheme")
)

// MapGRPCError maps a gRPC status error to the appropriate vault sentinel + cause.
//
// Mappings cover both server-emitted codes (rune-admin/vault/internal/server/
// grpc.go) and transport-layer codes (gRPC runtime / client deadline):
//
//	Unauthenticated     → ErrVaultAuthFailed       (token validation)
//	PermissionDenied    → ErrVaultPermissionDenied (role scope check)
//	InvalidArgument     → ErrVaultTopKExceeded     (msg contains "exceeds limit": top_k over role limit)
//	InvalidArgument     → ErrVaultInvalidInput     (any other bad client input)
//	ResourceExhausted   → ErrVaultRateLimited      (token rate limit)
//	NotFound            → ErrVaultKeyNotFound      (server doesn't emit this today; mapped for future)
//	Unavailable         → ErrVaultUnavailable      (transport: network / server down)
//	DeadlineExceeded    → ErrVaultTimeout          (transport: client deadline)
//	Internal / <other>  → ErrVaultInternal         (server failures + non-gRPC fallback)
//
// Returns nil for nil input.
func MapGRPCError(err error) error {
	if err == nil {
		return nil
	}

	st, ok := status.FromError(err)
	if !ok {
		return &Error{
			Code:      ErrVaultInternal.Code,
			Message:   err.Error(),
			Retryable: true,
			Cause:     err,
		}
	}

	switch st.Code() {
	case codes.Unauthenticated:
		return &Error{
			Code:      ErrVaultAuthFailed.Code,
			Message:   st.Message(),
			Retryable: false,
			Cause:     err,
		}
	case codes.PermissionDenied:
		return &Error{
			Code:      ErrVaultPermissionDenied.Code,
			Message:   st.Message(),
			Retryable: false,
			Cause:     err,
		}
	case codes.InvalidArgument:
		// The vault server returns codes.InvalidArgument both for genuinely
		// malformed input (base64 deserialization, oversized lists) and for a
		// top_k that exceeds the token role's limit. The latter carries the
		// message "top_k N exceeds limit M for role 'X'" (rune-admin
		// vault/internal/tokens/errors.go ErrTopKExceeded) — match both stable
		// substrings ("top_k" + "exceeds limit") so callers can surface a
		// dedicated TOPK_LIMIT error instead of a generic invalid-input one,
		// while staying robust against unrelated "exceeds ..." messages.
		if msg := st.Message(); strings.Contains(msg, "top_k") && strings.Contains(msg, "exceeds limit") {
			return &Error{
				Code:      ErrVaultTopKExceeded.Code,
				Message:   st.Message(),
				Retryable: false,
				Cause:     err,
			}
		}
		return &Error{
			Code:      ErrVaultInvalidInput.Code,
			Message:   st.Message(),
			Retryable: false,
			Cause:     err,
		}
	case codes.ResourceExhausted:
		return &Error{
			Code:      ErrVaultRateLimited.Code,
			Message:   st.Message(),
			Retryable: true,
			Cause:     err,
		}
	case codes.NotFound:
		return &Error{
			Code:      ErrVaultKeyNotFound.Code,
			Message:   st.Message(),
			Retryable: false,
			Cause:     err,
		}
	case codes.Unavailable:
		return &Error{
			Code:      ErrVaultUnavailable.Code,
			Message:   st.Message(),
			Retryable: true,
			Cause:     err,
		}
	case codes.DeadlineExceeded:
		return &Error{
			Code:      ErrVaultTimeout.Code,
			Message:   st.Message(),
			Retryable: true,
			Cause:     err,
		}
	default:
		return &Error{
			Code:      ErrVaultInternal.Code,
			Message:   st.Message(),
			Retryable: true,
			Cause:     err,
		}
	}
}
