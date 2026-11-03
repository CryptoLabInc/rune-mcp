package recovery

import (
	"context"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

type countingRetriggerer struct{ calls int }

func (r *countingRetriggerer) Retrigger() { r.calls++ }
func TestUnaryRecoveryPermanentErrorsDoNotReboot(t *testing.T) {
	for _, code := range []codes.Code{codes.DataLoss, codes.FailedPrecondition, codes.InvalidArgument, codes.Unauthenticated, codes.Unavailable} {
		t.Run(code.String(), func(t *testing.T) {
			r := &countingRetriggerer{}
			calls := 0
			failure := status.Error(code, "private cause")
			err := UnaryRecovery("console", r)(t.Context(), "/Search", nil, nil, nil, func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
				calls++
				return failure
			})
			want := 0
			if code == codes.Unavailable {
				want = 1
			}
			if err != failure || calls != 1 || r.calls != want {
				t.Fatalf("err=%v calls=%d reboots=%d;want %d", err, calls, r.calls, want)
			}
		})
	}
}
