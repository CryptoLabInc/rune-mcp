// Package service holds the orchestration layer — flows that coordinate adapters
// + policy. MCP tool handlers (internal/mcp/tools.go) delegate to these services;
// business logic lives here, not in handlers.
package service

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/seal"
	"github.com/CryptoLabInc/rune-mcp/internal/domain"
	"github.com/CryptoLabInc/rune-mcp/internal/lifecycle"
)

// CaptureDependencies is one boot generation. SetDependencies owns copies of
// key/capability slices; a running capture never observes partially replaced fields.
type CaptureDependencies struct {
	Console      console.Client
	Embedder     embedder.Client
	Encryptor    Encryptor
	Capabilities []string
	AgentID      string
	AgentDEK     []byte
	Author       string
}

// CaptureService orchestrates capture using one immutable boot snapshot.
type CaptureService struct {
	dependencies        atomic.Pointer[captureGeneration]
	generationMu        sync.Mutex
	resources           map[io.Closer]*captureResource
	stopped             bool
	lastManifestRefresh atomic.Int64
	State               *lifecycle.Manager
	Now                 func() time.Time
}

const manifestRefreshInterval = time.Minute

func (s *CaptureService) requireCapability(d *CaptureDependencies) error {
	if slices.Contains(d.Capabilities, "metadata_envelope_v2") {
		return nil
	}
	s.refreshManifest()
	return domain.ErrConsoleUpgradeRequired
}

// Refresh through the normal boot loop, at most once per minute. The failed
// capture is never resubmitted: its caller receives an actionable permanent error.
func (s *CaptureService) refreshManifest() {
	if s.State == nil || s.State.Current() != lifecycle.StateActive {
		return
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	last := s.lastManifestRefresh.Load()
	if last != 0 && now.UnixNano()-last < int64(manifestRefreshInterval) {
		return
	}
	if s.lastManifestRefresh.CompareAndSwap(last, now.UnixNano()) {
		s.State.Retrigger()
	}
}

func (s *CaptureService) captureError(err error) error {
	var ce *console.Error
	if errors.As(err, &ce) && ce.Code == console.ErrConsoleUpgradeRequired.Code {
		s.refreshManifest()
		return domain.ErrConsoleUpgradeRequired
	}
	return err
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
	d, release := s.acquireGeneration()
	defer release()
	return s.encryptSealInsert(ctx, d, text, func(*CaptureDependencies) (string, error) { return metadataJSON, nil })
}

func (s *CaptureService) encryptSealInsert(ctx context.Context, d *CaptureDependencies, text string, metadata func(*CaptureDependencies) (string, error)) (string, error) {
	if err := s.requireCapability(d); err != nil {
		return "", err
	}
	id := uuid.NewString()
	item, err := s.buildInsertItem(ctx, d, id, text, metadata)
	if err != nil {
		return "", err
	}
	insertedID, err := insertWithRecovery(ctx, s.State, d.Console, item)
	if err == nil || !isWrongCentroidVersion(err) {
		return insertedID, s.captureError(err)
	}
	// A C3 rejection permits rebuilding only for this original boot generation.
	// A later boot may belong to a different workspace even at the same URL.
	// Never move the caller's private content to that replacement destination.
	current, release := s.acquireGeneration()
	defer release()
	if cerr := s.requireCapability(current); cerr != nil {
		return "", cerr
	}
	if current != d {
		return "", &domain.RuneError{Code: domain.CodePipelineNotReady, Message: "Capture stopped because the Console connection changed", RecoveryHint: "Check /rune:status and confirm the intended workspace before submitting this capture again."}
	}

	slog.Warn("capture: insert rejected for stale centroid version; resyncing and retrying once", "id", id)
	if rerr := resyncCentroids(ctx, d); rerr != nil {
		return "", fmt.Errorf("insert rejected (%w) and centroid resync failed: %w", err, rerr)
	}
	item, err = s.buildInsertItem(ctx, d, id, text, metadata)
	if err != nil {
		return "", err
	}
	insertedID, err = insertWithRecovery(ctx, s.State, d.Console, item)
	return insertedID, s.captureError(err)
}

// buildInsertItem runs the client-side crypto pipeline — embed+route (runed) →
// encrypt (EncKey) → seal (agent_dek) — and assembles the console item under the
// given idempotent id. On runed FAILED_PRECONDITION (no centroid set — C4,
// e.g. the best-effort boot relay failed or runed restarted with a cold cache)
// it pushes the set once and retries the route.
func (s *CaptureService) buildInsertItem(ctx context.Context, d *CaptureDependencies, id, text string, metadata func(*CaptureDependencies) (string, error)) (console.InsertItem, error) {
	if d.Encryptor == nil || d.Embedder == nil || d.Console == nil {
		return console.InsertItem{}, fmt.Errorf("capture: dependencies not initialized")
	}
	metadataJSON, err := metadata(d)
	if err != nil {
		return console.InsertItem{}, err
	}
	routed, err := d.Embedder.EmbedRoute(ctx, text)
	if isNoCentroids(err) {
		slog.Warn("capture: runed has no centroid set; resyncing and retrying once")
		if rerr := resyncCentroids(ctx, d); rerr != nil {
			return console.InsertItem{}, fmt.Errorf("embed route: %w (centroid resync failed: %w)", err, rerr)
		}
		routed, err = d.Embedder.EmbedRoute(ctx, text)
	}
	if err != nil {
		return console.InsertItem{}, fmt.Errorf("embed route: %w", err)
	}
	rmp, err := d.Encryptor.EncryptFlat(routed.Vector)
	if err != nil {
		return console.InsertItem{}, fmt.Errorf("encrypt flat: %w", err)
	}
	mm, err := d.Encryptor.EncryptClustered(routed.Vector)
	if err != nil {
		return console.InsertItem{}, fmt.Errorf("encrypt clustered: %w", err)
	}
	sealed, err := seal.SealV2(d.AgentDEK, d.AgentID, id, []byte(metadataJSON))
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
// Flow: build the record → embed and route the insight locally → encrypt the
// vector and seal the record JSON → insert the encrypted item → respond.
// Capture does not measure novelty; separate calls may store duplicate records.
func (s *CaptureService) Handle(ctx context.Context, req *domain.CaptureRequest) (*domain.CaptureResponse, error) {
	d, release := s.acquireGeneration()
	defer release()
	if err := s.requireCapability(d); err != nil {
		return nil, err
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	recordID, err := s.encryptSealInsert(ctx, d, req.Insight, func(current *CaptureDependencies) (string, error) {
		if err := domain.ValidateCaptureContent(req.Insight, req.Context, current.Author); err != nil {
			return "", err
		}
		body, err := json.Marshal(domain.Record{Timestamp: now.UTC(), Author: current.Author, Insight: req.Insight, Context: req.Context})
		if err != nil {
			return "", fmt.Errorf("marshal record: %w", err)
		}
		return string(body), nil
	})
	if err != nil {
		return nil, fmt.Errorf("console insert: %w", err)
	}
	return &domain.CaptureResponse{OK: true, Captured: true, RecordID: recordID}, nil
}
