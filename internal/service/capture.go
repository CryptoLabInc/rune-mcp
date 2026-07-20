// Package service holds the orchestration layer — flows that coordinate adapters
// + policy. MCP tool handlers (internal/mcp/tools.go) delegate to these services;
// business logic lives here, not in handlers.
package service

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/seal"
	"github.com/CryptoLabInc/rune-mcp/internal/domain"
	"github.com/CryptoLabInc/rune-mcp/internal/lifecycle"
	"github.com/CryptoLabInc/rune-mcp/internal/policy"
)

// CaptureService orchestrates the capture flow.
type CaptureService struct {
	Console   console.Client
	Embedder  embedder.Client
	Encryptor Encryptor
	State     *lifecycle.Manager

	// Injected from Console bundle at boot.
	AgentID  string // for the seal envelope's "a" field
	AgentDEK []byte // metadata seal key (agent_dek from manifest)

	// Author is the record attribution ("Display Name <email>"), wired from the
	// console bundle.
	Author string

	Now func() time.Time // injectable clock (default: time.Now)
}

// Encryptor is the client-side FHE encrypt surface (runespacecrypto). Kept as
// an interface so capture tests need no cgo. EncryptFlat/EncryptClustered take
// an l2-normalized vector and return the tier ciphertexts.
type Encryptor interface {
	EncryptFlat(vec []float32) ([]byte, error)
	EncryptClustered(vec []float32) ([]byte, error)
}

// NewCaptureService constructs with default clock.
func NewCaptureService() *CaptureService {
	return &CaptureService{Now: time.Now}
}

// EncryptSealInsert embeds+routes text, encrypts the vector (EncKey), seals
// metadataJSON (agent_dek), and forwards the ciphertext item to the console
// under a fresh idempotent id. Returns the plaintext insert id.
//
// Centroid desync self-heals here: buildInsertItem covers C4 (runed has
// no set), and a WRONG_CENTROID_VERSION rejection covers C3 — resync, rebuild
// the item under the new set (fresh cluster_id + version, same id), and retry
// exactly once.
func (s *CaptureService) EncryptSealInsert(ctx context.Context, text, metadataJSON string) (string, error) {
	if s.Encryptor == nil {
		return "", fmt.Errorf("capture: encryptor not initialized")
	}
	id := uuid.NewString()
	item, err := s.buildInsertItem(ctx, id, text, metadataJSON)
	if err != nil {
		return "", err
	}
	insertedID, err := insertWithRecovery(ctx, s.State, s.Console, item)
	if err == nil || !isWrongCentroidVersion(err) {
		return insertedID, err
	}
	// C3: the engine replaced its centroid set after we routed. The console has
	// already dropped its stale cache, so a resync now yields the new set.
	slog.Warn("capture: insert rejected for stale centroid version; resyncing and retrying once", "id", id)
	if rerr := s.resyncCentroids(ctx); rerr != nil {
		return "", fmt.Errorf("insert rejected (%w) and centroid resync failed: %w", err, rerr)
	}
	item, err = s.buildInsertItem(ctx, id, text, metadataJSON)
	if err != nil {
		return "", err
	}
	return insertWithRecovery(ctx, s.State, s.Console, item)
}

// buildInsertItem runs the client-side crypto pipeline — embed+route (runed) →
// encrypt (EncKey) → seal (agent_dek) — and assembles the console item under the
// given idempotent id. On runed FAILED_PRECONDITION (no centroid set — C4,
// e.g. the best-effort boot relay failed or runed restarted with a cold cache)
// it pushes the set once and retries the route.
func (s *CaptureService) buildInsertItem(ctx context.Context, id, text, metadataJSON string) (console.InsertItem, error) {
	routed, err := s.Embedder.EmbedRoute(ctx, text)
	if isNoCentroids(err) {
		slog.Warn("capture: runed has no centroid set; resyncing and retrying once")
		if rerr := s.resyncCentroids(ctx); rerr != nil {
			return console.InsertItem{}, fmt.Errorf("embed route: %w (centroid resync failed: %w)", err, rerr)
		}
		routed, err = s.Embedder.EmbedRoute(ctx, text)
	}
	if err != nil {
		return console.InsertItem{}, fmt.Errorf("embed route: %w", err)
	}
	rmp, err := s.Encryptor.EncryptFlat(routed.Vector)
	if err != nil {
		return console.InsertItem{}, fmt.Errorf("encrypt flat: %w", err)
	}
	mm, err := s.Encryptor.EncryptClustered(routed.Vector)
	if err != nil {
		return console.InsertItem{}, fmt.Errorf("encrypt clustered: %w", err)
	}
	sealed, err := seal.Seal(s.AgentDEK, s.AgentID, []byte(metadataJSON))
	if err != nil {
		return console.InsertItem{}, fmt.Errorf("seal metadata: %w", err)
	}
	return console.InsertItem{
		ID:                 id,
		RMPItem:            rmp,
		MMItem:             mm,
		ClusterID:          routed.ClusterID,
		CentroidSetVersion: routed.CentroidSetVersion,
		SealedMetadata:     sealed,
	}, nil
}

// Handle captures one record. Called by internal/mcp/tools.go handleCapture.
//
// Flow: build the record → embed the insight → novelty gate on the insight
// embedding (near-duplicate short-circuits) → seal + insert the record JSON,
// keyed on the insight embedding → respond.
func (s *CaptureService) Handle(ctx context.Context, req *domain.CaptureRequest) (*domain.CaptureResponse, error) {
	if s.Encryptor == nil {
		return nil, fmt.Errorf("capture: encryptor not initialized")
	}

	record := domain.Record{
		Timestamp: s.Now().UTC(),
		Author:    s.Author,
		Insight:   req.Insight,
		Context:   req.Context,
	}

	noveltyInfo, earlyResp, _ := s.runNoveltyCheck(ctx, record.Insight)
	if earlyResp != nil {
		return earlyResp, nil // near_duplicate
	}
	if noveltyInfo == nil {
		noveltyInfo = &domain.NoveltyInfo{Score: 1.0, Class: "novel"}
	}

	body, err := json.Marshal(record)
	if err != nil {
		return nil, fmt.Errorf("marshal record: %w", err)
	}
	recordID, err := s.EncryptSealInsert(ctx, record.Insight, string(body))
	if err != nil {
		return nil, fmt.Errorf("console insert: %w", err)
	}

	return &domain.CaptureResponse{
		OK:       true,
		Captured: true,
		RecordID: recordID,
		Novelty:  noveltyInfo,
	}, nil
}

// runNoveltyCheck embeds the insight, searches for near neighbors, and classifies
// novelty. Returns novelty info + nil if capture should proceed, or a pre-built
// response if near_duplicate (caller short-circuits). All failures are non-fatal.
func (s *CaptureService) runNoveltyCheck(ctx context.Context, insight string) (*domain.NoveltyInfo, *domain.CaptureResponse, error) {
	if s.Embedder == nil || s.Console == nil {
		return &domain.NoveltyInfo{Score: 1.0, Class: "novel"}, nil, nil
	}

	vec, err := s.Embedder.EmbedSingle(ctx, insight)
	if err != nil {
		slog.Warn("novelty check: embed failed (non-fatal)", "err", err)
		return &domain.NoveltyInfo{Score: 1.0, Class: "novel"}, nil, nil
	}

	hits, err := searchWithRecovery(ctx, s.State, s.Console, vec, 3)
	if err != nil || len(hits) == 0 {
		slog.Warn("novelty check: search failed or empty (non-fatal)", "err", err)
		return &domain.NoveltyInfo{Score: 1.0, Class: "novel"}, nil, nil
	}

	maxSim := 0.0
	for _, h := range hits {
		if h.Score > maxSim {
			maxSim = h.Score
		}
	}

	class, score := policy.ClassifyNovelty(maxSim, policy.DefaultNoveltyThresholds)
	noveltyInfo := &domain.NoveltyInfo{
		Score:   score,
		Class:   class,
		Related: buildRelatedTop3(hits),
	}

	if class == domain.NoveltyClassNearDuplicate {
		return noveltyInfo, &domain.CaptureResponse{
			OK:       true,
			Captured: false,
			Reason:   "Near-duplicate - virtually identical insight already stored",
			Novelty:  noveltyInfo,
		}, nil
	}

	return noveltyInfo, nil, nil
}

func buildRelatedTop3(hits []console.Hit) []domain.RelatedRecord {
	n := len(hits)
	if n > 3 {
		n = 3
	}

	records := make([]domain.RelatedRecord, n)
	for i := 0; i < n; i++ {
		records[i] = domain.RelatedRecord{
			ID:         hits[i].ID,
			Similarity: math.Round(hits[i].Score*1000) / 1000,
		}
	}

	return records
}
