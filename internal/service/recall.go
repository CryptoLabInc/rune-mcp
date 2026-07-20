package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	"github.com/CryptoLabInc/rune-mcp/internal/domain"
	"github.com/CryptoLabInc/rune-mcp/internal/lifecycle"
	"github.com/CryptoLabInc/rune-mcp/internal/policy"
)

// RecallService orchestrates the recall flow.
type RecallService struct {
	Console  console.Client
	Embedder embedder.Client
	State    *lifecycle.Manager
	Now      func() time.Time
}

// NewRecallService constructs with default clock.
func NewRecallService() *RecallService {
	return &RecallService{Now: time.Now}
}

// External-IO call deadlines (per call, not aggregate).
//   - embedder: runed <100ms warm; 10s tolerates ggml cold start.
//   - console Search: the console runs the blind search + FHE decrypt against
//     runespace, the heaviest hop; 30s upper bound.
const (
	embedderCallTimeout  = 10 * time.Second
	consoleSearchTimeout = 30 * time.Second
)

// Handle runs the recall flow: embed the query, search once, recency-weight the
// hits, and truncate to topK.
func (s *RecallService) Handle(ctx context.Context, args *domain.RecallArgs) (*domain.RecallResult, error) {
	embedCtx, cancel := context.WithTimeout(ctx, embedderCallTimeout)
	vec, err := s.Embedder.EmbedSingle(embedCtx, args.Query)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("embed query: %w", err)
	}

	topK := args.TopK
	if topK <= 0 {
		topK = 5
	}

	hits, err := s.search(ctx, vec, topK)
	if err != nil {
		return nil, fmt.Errorf("search: %w", err)
	}

	hits = policy.ApplyRecencyWeighting(hits, s.Now())
	if len(hits) > topK {
		hits = hits[:topK]
	}

	return buildResult(hits), nil
}

// topKLimitErr returns a domain TOPK_LIMIT error if err wraps a console top_k
// limit rejection, else nil. A top_k over the token's role limit is
// deterministic, so recall aborts and surfaces a distinct, actionable error
// instead of a generic INVALID_INPUT.
func topKLimitErr(err error) *domain.RuneError {
	var ve *console.Error
	if errors.As(err, &ve) && ve.Code == console.ErrConsoleTopKExceeded.Code {
		return &domain.RuneError{
			Code:      domain.CodeTopKLimit,
			Message:   ve.Message,
			Retryable: false,
		}
	}
	return nil
}

// search runs one console Search and resolves each hit's (already plaintext)
// metadata into a SearchHit. The console does the whole blind search + FHE
// decrypt + metadata open internally.
func (s *RecallService) search(ctx context.Context, vec []float32, topk int) ([]domain.SearchHit, error) {
	searchCtx, cancel := context.WithTimeout(ctx, consoleSearchTimeout)
	hits, err := searchWithRecovery(searchCtx, s.State, s.Console, vec, topk)
	cancel()
	if err != nil {
		if te := topKLimitErr(err); te != nil {
			return nil, te
		}
		slog.Warn("recall: console search failed", "err", err)
		return nil, fmt.Errorf("console search: %w", err)
	}
	slog.Info("recall: console search returned", "hits", len(hits), "topk", topk)
	return resolveHits(hits), nil
}

// resolveHits converts console hits into SearchHits. The console already decrypted
// the FHE scores and opened the metadata envelope, so Hit.Metadata is plaintext
// JSON — we just parse it. The record_id is the plaintext hit id, not a metadata
// field.
func resolveHits(hits []console.Hit) []domain.SearchHit {
	out := make([]domain.SearchHit, 0, len(hits))
	for _, h := range hits {
		if h.Metadata == "" {
			continue
		}
		var m map[string]any
		if err := json.Unmarshal([]byte(h.Metadata), &m); err != nil {
			continue
		}
		out = append(out, toSearchHit(h.ID, m, h.Score))
	}
	return out
}

// toSearchHit maps a hit id + decrypted metadata + score into a domain.SearchHit.
func toSearchHit(id string, metadata map[string]any, score float64) domain.SearchHit {
	return domain.SearchHit{
		RecordID: id,
		Author:   strFromMap(metadata, "author", ""),
		Insight:  strFromMap(metadata, "insight", ""),
		Context:  strFromMap(metadata, "context", ""),
		Score:    score,
		Metadata: metadata,
	}
}

func strFromMap(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

// buildResult assembles the final recall response.
func buildResult(results []domain.SearchHit) *domain.RecallResult {
	entries := make([]domain.RecallEntry, len(results))
	for i, h := range results {
		entries[i] = domain.RecallEntry{
			RecordID:      h.RecordID,
			Author:        h.Author,
			Insight:       h.Insight,
			Context:       h.Context,
			Score:         h.Score,
			AdjustedScore: h.AdjustedScore,
		}
	}

	return &domain.RecallResult{
		OK:      true,
		Found:   len(results),
		Results: entries,
	}
}
