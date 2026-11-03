package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"slices"
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
	s := NewRecallService()
	s.Console = c
	s.State = state
	s.Embedder = &privacyEmbedder{resyncEmbedder: &resyncEmbedder{version: "v2"}}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	hits, err := s.Handle(ctx, &domain.RecallArgs{Query: "private recall", TopK: 5})
	if c.calls != 1 {
		t.Errorf("integrity failure repeated search %d times; want 1", c.calls)
	}
	if hits != nil || err == nil {
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

type retryInsertConsole struct {
	console.Client
	items      []console.InsertItem
	firstError error
}

func (c *retryInsertConsole) Insert(_ context.Context, item console.InsertItem) (string, error) {
	snapshot := item
	snapshot.RMPItem = slices.Clone(item.RMPItem)
	snapshot.MMItem = slices.Clone(item.MMItem)
	c.items = append(c.items, snapshot)
	if len(c.items) == 1 {
		return "", c.firstError
	}
	return item.ID, nil
}

func TestInsertTransportRetryKeepsExactItem(t *testing.T) {
	state := lifecycle.NewManager()
	state.SetState(lifecycle.StateActive)
	c := &retryInsertConsole{firstError: console.MapGRPCError(status.Error(codes.Unavailable, "temporary transport failure"))}
	original := console.InsertItem{ID: "same-id", RMPItem: []byte{1, 2}, MMItem: []byte{3, 4}, ClusterID: 5, CentroidSetVersion: "version", SealedMetadata: "same-sealed-bytes"}
	expected, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	id, err := insertWithRecovery(t.Context(), state, c, original)
	if err != nil || id != original.ID || len(c.items) != 2 {
		t.Fatalf("retry result id=%s err=%v calls=%d", id, err, len(c.items))
	}
	for _, item := range c.items {
		got, err := json.Marshal(item)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, expected) {
			t.Fatalf("retry changed item: %+v", item)
		}
	}
}

func TestInsertUpgradeRequiredDoesNotRetry(t *testing.T) {
	state := lifecycle.NewManager()
	state.SetState(lifecycle.StateActive)
	c := &retryInsertConsole{firstError: console.MapGRPCError(status.Error(codes.FailedPrecondition, "metadata upgrade required: upgrade rune-mcp"))}
	s := newResyncService(&resyncEmbedder{version: "v2"}, &resyncConsole{engineVersion: "v2"})
	d := s.Dependencies()
	d.Console = c
	s.SetDependencies(d)
	s.State = state
	_, err := s.Handle(t.Context(), &domain.CaptureRequest{Insight: "upgrade refusal"})
	var re *domain.RuneError
	if !errors.As(err, &re) || re.Code != domain.CodeConsoleUpgradeRequired || re.Retryable || re.RecoveryHint == "" {
		t.Fatalf("upgrade refusal = %v", err)
	}
	if len(c.items) != 1 {
		t.Fatalf("upgrade refusal retried %d times", len(c.items))
	}
}

func TestCaptureCapabilityRefreshRateLimited(t *testing.T) {
	state := lifecycle.NewManager()
	state.SetState(lifecycle.StateActive)
	refreshes := 0
	state.SetReloadFunc(func() { refreshes++; state.SetState(lifecycle.StateActive) })
	now := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	s := NewCaptureService()
	s.State = state
	s.Now = func() time.Time { return now }
	for range 10 {
		if _, err := s.Handle(t.Context(), &domain.CaptureRequest{Insight: "blocked"}); err == nil {
			t.Fatal("missing capability accepted")
		}
	}
	if refreshes != 1 {
		t.Fatalf("refreshes=%d;want 1", refreshes)
	}
	now = now.Add(time.Minute)
	_, _ = s.Handle(t.Context(), &domain.CaptureRequest{Insight: "blocked"})
	if refreshes != 2 {
		t.Fatalf("refreshes after interval=%d;want 2", refreshes)
	}
}
