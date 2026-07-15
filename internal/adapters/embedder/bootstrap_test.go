package embedder

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func preconditionWithReason(t *testing.T, reason, msg string) error {
	t.Helper()
	st, err := status.New(codes.FailedPrecondition, msg).
		WithDetails(&errdetails.ErrorInfo{Reason: reason, Domain: "runed.v1"})
	if err != nil {
		t.Fatalf("build status: %v", err)
	}
	return st.Err()
}

// The mapper must split runed's two FAILED_PRECONDITION conditions by reason;
// an untagged FailedPrecondition maps to an internal error.
func TestMapGRPCError_FailedPreconditionByReason(t *testing.T) {
	cases := []struct {
		name      string
		in        error
		wantCode  string
		retryable bool
	}{
		{"bootstrapping", preconditionWithReason(t, "BOOTSTRAPPING", "daemon is bootstrapping"), ErrEmbedderBootstrapping.Code, true},
		{"no centroid set", preconditionWithReason(t, "NO_CENTROID_SET", "no centroid set loaded"), ErrEmbedderNoCentroids.Code, false},
		{"untagged FailedPrecondition", status.Error(codes.FailedPrecondition, "unexpected precondition"), ErrEmbedderInternal.Code, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var e *Error
			if !errors.As(MapGRPCError(tc.in), &e) {
				t.Fatal("not an embedder.Error")
			}
			if e.Code != tc.wantCode || e.Retryable != tc.retryable {
				t.Fatalf("got (%s, retryable=%v), want (%s, retryable=%v)", e.Code, e.Retryable, tc.wantCode, tc.retryable)
			}
		})
	}
}

func shrinkBootstrapWait(t *testing.T, budget, poll time.Duration) {
	t.Helper()
	pb, pp := BootstrapWaitBudget, BootstrapPollEvery
	BootstrapWaitBudget, BootstrapPollEvery = budget, poll
	t.Cleanup(func() { BootstrapWaitBudget, BootstrapPollEvery = pb, pp })
}

// The retry layer must wait out the bootstrap window: keep polling (without
// consuming retry attempts) until the daemon answers, then return the result.
func TestRetry_WaitsOutBootstrap(t *testing.T) {
	shrinkBootstrapWait(t, 2*time.Second, 5*time.Millisecond)
	calls := 0
	r, err := retry(context.Background(), func(context.Context) (string, error) {
		calls++
		if calls <= 4 {
			return "", preconditionWithReason(t, "BOOTSTRAPPING", "loading")
		}
		return "ok", nil
	})
	if err != nil || r != "ok" {
		t.Fatalf("want ok after bootstrap, got %q err=%v", r, err)
	}
	if calls != 5 {
		t.Fatalf("calls=%d; want 5 (4 bootstrap polls + success)", calls)
	}
}

// A budget overrun must surface the retryable EMBEDDER_BOOTSTRAPPING error —
// never spin forever, never mislabel as the centroid case.
func TestRetry_BootstrapBudgetExhausted(t *testing.T) {
	shrinkBootstrapWait(t, 30*time.Millisecond, 5*time.Millisecond)
	_, err := retry(context.Background(), func(context.Context) (string, error) {
		return "", preconditionWithReason(t, "BOOTSTRAPPING", "loading")
	})
	var e *Error
	if !errors.As(err, &e) {
		t.Fatalf("not an embedder.Error: %v", err)
	}
	if e.Code != ErrEmbedderBootstrapping.Code || !e.Retryable {
		t.Fatalf("got (%s, retryable=%v), want (EMBEDDER_BOOTSTRAPPING, true)", e.Code, e.Retryable)
	}
}
