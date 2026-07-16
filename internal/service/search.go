package service

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	"github.com/CryptoLabInc/rune-mcp/internal/domain"
)

// SearchByID — shared helper used by delete_capture.
//
// Embeds "ID: {record_id}" as a query and searches top-5 via the console, then
// filters results by exact record_id match. Relies on the self-embedding
// surfacing the target record. Metadata comes back plaintext from the console.
func SearchByID(
	ctx context.Context,
	embedderClient embedder.Client,
	consoleClient console.Client,
	recordID string,
) (*domain.SearchHit, error) {
	query := fmt.Sprintf("ID: %s", recordID)

	vec, err := embedderClient.EmbedSingle(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("search by ID: embed: %w", err)
	}

	hits, err := consoleClient.Search(ctx, vec, 5)
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
