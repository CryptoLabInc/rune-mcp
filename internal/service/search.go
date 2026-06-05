package service

import (
	"context"
	"encoding/base64"
	"fmt"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/envector"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/vault"
	"github.com/CryptoLabInc/rune-mcp/internal/domain"
)

// SearchByID — shared helper used by delete_capture (lifecycle §5) and, if
// needed, by recall. Python: agents/retriever/searcher.py:L561-567.
//
// Hack: embed "ID: {record_id}" as query and search top-5 via standard pipeline,
// then filter results by exact record_id match. Relies on envector similarity
// surfacing the target record for its self-embedding. Kept as-is under D25/D27
// bit-identical principle.
func SearchByID(
	ctx context.Context,
	embedderClient embedder.Client,
	vaultClient vault.Client,
	envClient envector.Client,
	indexName string,
	recordID string,
) (*domain.SearchHit, error) {
	query := fmt.Sprintf("ID: %s", recordID)

	vec, err := embedderClient.EmbedSingle(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("search by ID: embed: %w", err)
	}

	// Score
	blobs, err := envClient.Score(ctx, vec)
	if err != nil {
		return nil, fmt.Errorf("search by ID: score: %w", err)
	}
	if len(blobs) == 0 {
		return nil, nil
	}

	// Decrypt scores. The Vault RPC field is `EncryptedBlobB64`
	// (proto3 `string`, valid-UTF-8 only) — envector returns raw cipher
	// bytes, so we must base64-encode before sending. A direct
	// `string(blobs[0])` cast pushes random cipher bytes through the
	// proto3 string-validation path and trips
	// "grpc: error while marshaling: string field contains invalid UTF-8".
	// Mirrors recall.searchSingle and capture.
	encryptedBlobB64 := base64.StdEncoding.EncodeToString(blobs[0])
	entries, err := vaultClient.DecryptScores(ctx, encryptedBlobB64, 5)
	if err != nil {
		return nil, fmt.Errorf("search by ID: decrypt scores: %w", err)
	}

	// Get metadata
	refs := make([]envector.MetadataRef, len(entries))
	for i, e := range entries {
		refs[i] = envector.MetadataRef{ShardIdx: uint64(e.ShardIdx), RowIdx: uint64(e.RowIdx)}
	}
	metaEntries, err := envClient.GetMetadata(ctx, refs, []string{"metadata"})
	if err != nil {
		return nil, fmt.Errorf("search by ID: get metadata: %w", err)
	}

	// Resolve + filter by record_id
	for i, me := range metaEntries {
		score := 0.0
		if i < len(entries) {
			score = entries[i].Score
		}
		_, parsed := classifyMetadata(me.Data)
		if parsed == nil {
			continue
		}
		hit := toSearchHit(parsed, score)
		if hit.RecordID == recordID {
			return &hit, nil
		}
	}

	return nil, nil // not found
}
