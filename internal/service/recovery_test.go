package service

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/domain"
	"github.com/CryptoLabInc/rune-mcp/internal/lifecycle"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

// Only the external Console boundary is replaced; the gRPC mapping, recall
// search, recovery helper and active lifecycle manager are production code.
type integritySearchConsole struct {
	console.Client
	calls int
}

func (c *integritySearchConsole) Search(_ context.Context, vec []float32, topK int) ([]console.Hit, error) {
	c.calls++
	return nil, console.MapGRPCError(status.Error(codes.DataLoss, "private record text must not escape"))
}

func TestRecallIntegrityFailureDoesNotRetrySearch(t *testing.T) {
	state := lifecycle.NewManager()
	state.SetState(lifecycle.StateActive)
	c := &integritySearchConsole{}
	s := &RecallService{Console: c, State: state}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	hits, err := s.search(ctx, []float32{1}, 5)
	if c.calls != 1 {
		t.Errorf("integrity failure repeated search %d times; want 1", c.calls)
	}
	if len(hits) != 0 || err == nil {
		t.Fatalf("integrity failure returned hits=%v error=%v", hits, err)
	}
	var de *domain.RuneError
	if !errors.As(err, &de) || de.Code != domain.CodeConsoleDecryption || de.Retryable {
		t.Fatalf("expected permanent domain decryption error, got %v", err)
	}
	if strings.Contains(err.Error(), "private record text") {
		t.Fatal("integrity error disclosed record text")
	}
}
