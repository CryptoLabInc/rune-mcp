package embedder

import (
	"context"
	"fmt"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Bootstrap wait — the first start of the runed daemon (e.g. the first
// session after a machine reboot) loads the embedding model for tens of
// seconds and answers FAILED_PRECONDITION/BOOTSTRAPPING meanwhile. That state
// heals by itself, so instead of failing the caller's capture/recall, retry
// blocks on it: poll every BootstrapPollEvery up to BootstrapWaitBudget, then
// surface the (retryable) error. Vars, not consts, so tests can shrink them.
// Distinct from the D7 schedule: D7 covers transient transport faults;
// bootstrap polling never consumes D7 attempts.
var (
	BootstrapWaitBudget = 60 * time.Second
	BootstrapPollEvery  = 2500 * time.Millisecond
)

// retry executes call() with the D7 backoff schedule [0, 500ms, 2s].
//
// Total attempts: 3 (one per RetryBackoffs entry).
//
// Retryable gRPC codes:
//
//	Unavailable          — daemon restart / transient network
//	DeadlineExceeded     — per-call timeout hit
//	ResourceExhausted    — daemon overloaded
//
// Non-retryable errors (e.g., InvalidArgument) return immediately — except
// the BOOTSTRAPPING precondition, which enters the bootstrap wait above.
//
// On any non-nil return, errors are wrapped via MapGRPCError so the service
// layer can detect adapter retryable failures (typed embedder.Error with Retryable=true)
func retry[R any](ctx context.Context, call func(context.Context) (R, error)) (R, error) {
	var zero R
	var lastErr error
	var bootstrapDeadline time.Time // zero until the first BOOTSTRAPPING answer
	for i := 0; i < len(RetryBackoffs); {
		if delay := RetryBackoffs[i]; delay > 0 {
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return zero, MapGRPCError(ctx.Err())
			}
		}
		r, err := call(ctx)
		if err == nil {
			return r, nil
		}
		if isBootstrapping(err) {
			if bootstrapDeadline.IsZero() {
				bootstrapDeadline = time.Now().Add(BootstrapWaitBudget)
			}
			if time.Now().After(bootstrapDeadline) {
				return zero, MapGRPCError(err) // budget spent — surfaces retryable EMBEDDER_BOOTSTRAPPING
			}
			select {
			case <-time.After(BootstrapPollEvery):
			case <-ctx.Done():
				return zero, MapGRPCError(ctx.Err())
			}
			continue // waiting out a known-transient state consumes no D7 attempt
		}
		if !retryable(err) {
			return zero, MapGRPCError(err)
		}
		lastErr = err
		i++
	}
	return zero, MapGRPCError(fmt.Errorf("embedder: all retries exhausted: %w", lastErr))
}

// isBootstrapping matches runed's model-loading precondition by its ErrorInfo
// reason (legacy reason-less runed never matches — those fall through to the
// centroid-case mapping as before).
func isBootstrapping(err error) bool {
	return status.Code(err) == codes.FailedPrecondition && grpcErrorReason(err) == reasonBootstrapping
}

// retryable returns true for transient gRPC codes.
//
//	Unavailable / DeadlineExceeded / ResourceExhausted → true
//	other (including non-gRPC errors)                   → false
func retryable(err error) bool {
	st, ok := status.FromError(err)
	if !ok {
		return false
	}
	switch st.Code() {
	case codes.Unavailable, codes.DeadlineExceeded, codes.ResourceExhausted:
		return true
	}
	return false
}
