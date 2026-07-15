package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/vault"
	"github.com/CryptoLabInc/rune-mcp/internal/domain"
)

// SearchByID — shared helper used by delete_capture (lifecycle §5).
//
// Embeds "ID: {record_id}" as a query and searches top-5 via the vault, then
// filters results by exact record_id match. Relies on the self-embedding
// surfacing the target record. Metadata comes back plaintext from the vault.
func SearchByID(
	ctx context.Context,
	embedderClient embedder.Client,
	vaultClient vault.Client,
	indexName string,
	recordID string,
) (*domain.SearchHit, error) {
	query := fmt.Sprintf("ID: %s", recordID)

	vec, err := embedderClient.EmbedSingle(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("search by ID: embed: %w", err)
	}

	hits, err := vaultClient.Search(ctx, vec, 5)
	if err != nil {
		return nil, fmt.Errorf("search by ID: search: %w", err)
	}

	for _, h := range hits {
		if h.Metadata == "" {
			continue
		}
		var m map[string]any
		if json.Unmarshal([]byte(h.Metadata), &m) != nil {
			continue
		}
		hit := toSearchHit(m, h.Score)
		if hit.RecordID == recordID {
			return &hit, nil
		}
	}
	return nil, nil // not found
}
