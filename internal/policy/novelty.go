// Package policy holds pure functions — novelty classification and the recall
// rerank formula. No I/O, no external deps.
package policy

import (
	"math"

	"github.com/CryptoLabInc/rune-mcp/internal/domain"
)

// NoveltyThresholds — runtime defaults.
// The 0.4/0.7/0.93 module constants are dead defaults; the runtime passes these
// values explicitly at the call site.
type NoveltyThresholds struct {
	Novel   float64
	Related float64
	NearDup float64
}

// DefaultNoveltyThresholds — runtime values.
var DefaultNoveltyThresholds = NoveltyThresholds{
	Novel:   0.3,
	Related: 0.7,
	NearDup: 0.95,
}

// ClassifyNovelty classifies a candidate against its max similarity.
//
// Returns (class, score) where score = round(1.0 - maxSimilarity, 4)
// (inverted — higher score means more novel).
//
//   - similarity <  0.3  → novel
//   - 0.3 ≤ sim <  0.7   → evolution
//   - 0.7 ≤ sim <  0.95  → related
//   - sim ≥ 0.95         → near_duplicate (capture blocked)
func ClassifyNovelty(maxSimilarity float64, th NoveltyThresholds) (domain.NoveltyClass, float64) {
	noveltyScore := 1.0 - maxSimilarity
	score := math.Round(noveltyScore*10000) / 10000

	if maxSimilarity >= th.NearDup {
		return domain.NoveltyClassNearDuplicate, score
	} else if maxSimilarity >= th.Related {
		return domain.NoveltyClassRelated, score
	} else if maxSimilarity >= th.Novel {
		return domain.NoveltyClassEvolution, score
	}

	return domain.NoveltyClassNovel, score
}
