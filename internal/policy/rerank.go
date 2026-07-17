package policy

import (
	"math"
	"sort"
	"time"

	"github.com/CryptoLabInc/rune-mcp/internal/domain"
)

// Rerank constants.
// Formula: SimilarityWeight × raw + RecencyWeight × decay
// Half-life decay: decay = 0.5 ^ (age_days / HalfLifeDays)
const (
	HalfLifeDays     = 90.0
	SimilarityWeight = 0.7
	RecencyWeight    = 0.3
)

// ApplyRecencyWeighting adjusts each hit's score by recency (decay only).
//
// For each hit, adjusted_score = 0.7 × raw + 0.3 × decay
// where decay = 0.5 ^ (age_days / 90) and age_days = math.Floor((now - ts).days).
// Sorts by adjusted_score descending (stable).
//
// Age is floored to whole days: Go Hours()/24 is float — math.Floor gives the
// whole-day age the decay expects.
func ApplyRecencyWeighting(hits []domain.SearchHit, now time.Time) []domain.SearchHit {
	for i := range hits {
		r := &hits[i]
		ageDays := 0.0
		if tsStr, ok := r.Metadata["timestamp"].(string); ok {
			if ts, err := time.Parse(time.RFC3339, tsStr); err == nil {
				ageDays = math.Max(0, math.Floor(now.Sub(ts).Hours()/24))
			}
		} else if tsFloat, ok := r.Metadata["timestamp"].(float64); ok {
			ts := time.Unix(int64(tsFloat), 0).UTC()
			ageDays = math.Max(0, math.Floor(now.Sub(ts).Hours()/24))
		}

		decay := 1.0
		if HalfLifeDays > 0 {
			decay = math.Pow(0.5, ageDays/HalfLifeDays)
		}
		r.AdjustedScore = SimilarityWeight*r.Score + RecencyWeight*decay
	}

	sort.SliceStable(hits, func(i, j int) bool {
		return hits[i].AdjustedScore > hits[j].AdjustedScore
	})
	return hits
}
