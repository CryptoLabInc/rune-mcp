package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/vault"
)

// Centroid self-heal paths (README §9.2). The boot-time relay is best-effort,
// and the engine may replace its centroid set while we run; both cases are
// recovered here, at the point of failure, with exactly one retry each —
// never a loop:
//
//	C4: runed answers FAILED_PRECONDITION (no set injected)
//	    → resync (vault relay → runed) → retry EmbedRoute once
//	C3: vault Insert answers WRONG_CENTROID_VERSION (set was replaced)
//	    → resync → re-route + re-encrypt under the new set → retry the same id once

// resyncCentroids pulls the current centroid set through the vault relay and
// pushes it to runed. The vault drops its own SDK cache when the engine
// rejects a version (ForwardInsert), so a post-rejection fetch yields the
// fresh set, not the stale cache.
func (s *CaptureService) resyncCentroids(ctx context.Context) error {
	cs, err := s.Vault.Centroids(ctx)
	if err != nil {
		return fmt.Errorf("centroid resync: relay fetch: %w", err)
	}
	if cs == nil || cs.Version == "" || len(cs.Vectors) == 0 {
		return fmt.Errorf("centroid resync: vault relayed an empty set")
	}
	if err := s.Embedder.SetCentroids(ctx, cs.Version, cs.Dim, cs.Vectors); err != nil {
		return fmt.Errorf("centroid resync: push to runed: %w", err)
	}
	slog.Info("capture: centroid set resynced to runed", "version", cs.Version, "nlist", len(cs.Vectors))
	return nil
}

// isNoCentroids matches C4: runed has no centroid set loaded.
func isNoCentroids(err error) bool {
	var e *embedder.Error
	return errors.As(err, &e) && e.Code == embedder.ErrEmbedderNoCentroids.Code
}

// isWrongCentroidVersion matches C3: the engine replaced its centroid set.
func isWrongCentroidVersion(err error) bool {
	var e *vault.Error
	return errors.As(err, &e) && e.Code == vault.ErrVaultWrongCentroidVersion.Code
}
