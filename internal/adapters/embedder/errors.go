package embedder

import (
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Mirrors vault.Error and envectorError
type Error struct {
	Code      string
	Message   string
	Retryable bool
	Cause     error
}

func (e *Error) Error() string {
	if e.Message == "" {
		return e.Code
	}
	return e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.Cause }

var (
	// Retryable
	ErrEmbedderUnavailable = &Error{Code: "EMBEDDER_UNAVAILABLE", Retryable: true} // daemon down or connection fail
	ErrEmbedderTimeout     = &Error{Code: "EMBEDDER_TIMEOUT", Retryable: true}

	// ErrEmbedderBootstrapping — runed is up but its model is still loading
	// (reason BOOTSTRAPPING; e.g. the first daemon start after a machine
	// reboot). Transient: the same call succeeds once loading finishes. The
	// retry layer waits this out (bootstrap wait, retry.go); if the budget
	// runs out the error surfaces retryable so the agent knows to try again.
	ErrEmbedderBootstrapping = &Error{Code: "EMBEDDER_BOOTSTRAPPING", Retryable: true}

	// Non-retryable
	ErrEmbedderInternal = &Error{Code: "EMBEDDER_INTERNAL", Retryable: false}
	// ErrEmbedderNoCentroids — runed has no centroid set loaded, surfaced as
	// FAILED_PRECONDITION by Embed with_route. Not retryable as-is: push a set
	// via SetCentroids first, then retry (§9.2 C4 — capture self-heals this way).
	ErrEmbedderNoCentroids = &Error{Code: "EMBEDDER_NO_CENTROIDS", Retryable: false}
)

// runed tags its two FAILED_PRECONDITION conditions with an ErrorInfo reason
// (runed internal/server, domain "runed.v1") so clients can branch without
// parsing the human message.
const reasonBootstrapping = "BOOTSTRAPPING"

// grpcErrorReason returns the ErrorInfo reason attached to a status error,
// or "" when absent (legacy runed builds predate the tagging).
func grpcErrorReason(err error) string {
	st, ok := status.FromError(err)
	if !ok {
		return ""
	}
	for _, d := range st.Details() {
		if info, ok := d.(*errdetails.ErrorInfo); ok {
			return info.GetReason()
		}
	}
	return ""
}

// Converts gRPC status error into typed embedder.Error
func MapGRPCError(err error) error {
	if err == nil {
		return nil // nil-safe
	}

	st, ok := status.FromError(err)
	if !ok {
		return &Error{
			Code:      ErrEmbedderInternal.Code,
			Message:   err.Error(),
			Retryable: false,
			Cause:     err,
		}
	}

	switch st.Code() {
	case codes.Unavailable, codes.ResourceExhausted:
		return &Error{
			Code:      ErrEmbedderUnavailable.Code,
			Message:   st.Message(),
			Retryable: true,
			Cause:     err,
		}
	case codes.DeadlineExceeded:
		return &Error{
			Code:      ErrEmbedderTimeout.Code,
			Message:   st.Message(),
			Retryable: true,
			Cause:     err,
		}
	case codes.FailedPrecondition:
		// runed uses FailedPrecondition for two opposite conditions, told
		// apart by the ErrorInfo reason: BOOTSTRAPPING (model loading — wait
		// and retry) vs NO_CENTROID_SET (push a set — §9.2 C4). A missing
		// reason means a legacy runed; fall back to the centroid case, which
		// preserves the pre-reason behavior.
		if grpcErrorReason(err) == reasonBootstrapping {
			return &Error{
				Code:      ErrEmbedderBootstrapping.Code,
				Message:   st.Message(),
				Retryable: true,
				Cause:     err,
			}
		}
		return &Error{
			Code:      ErrEmbedderNoCentroids.Code,
			Message:   st.Message(),
			Retryable: false,
			Cause:     err,
		}
	default:
		return &Error{
			Code:      ErrEmbedderInternal.Code,
			Message:   st.Message(),
			Retryable: false,
			Cause:     err,
		}
	}
}

// Expose Error to errors.As via Unwrap
var _ interface{ Unwrap() error } = (*Error)(nil)
