package console_test

import (
	"errors"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"testing"
)

func TestMetadataUpgradeRequiredIsPermanent(t *testing.T) {
	cause := status.Error(codes.FailedPrecondition, "metadata upgrade required: upgrade rune-mcp")
	err := console.MapGRPCError(cause)
	var ce *console.Error
	if !errors.As(err, &ce) || ce.Code != "CONSOLE_UPGRADE_REQUIRED" || ce.Retryable {
		t.Fatalf("upgrade refusal = %v", err)
	}
	if !errors.Is(err, cause) {
		t.Fatal("lost gRPC cause")
	}
}
