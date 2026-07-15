package policy

import (
	"math"
	"sort"
	"time"

	"github.com/CryptoLabInc/rune-mcp/internal/domain"
)

// Rerank constants.
// Formula: (SimilarityWeight × raw + RecencyWeight × decay) × statusMul
// Half-life decay: decay = 0.5 ^ (age_days / HalfLifeDays)
const (
	HalfLifeDays     = 90.0
	SimilarityWeight = 0.7
	RecencyWeight    = 0.3
)

// StatusMultiplier maps record status to a score multiplier. Unknown status → 1.0.
var StatusMultiplier = map[string]float64{
	"accepted":   1.0,
	"proposed":   0.9,
	"superseded": 0.5,
	"reverted":   0.3,
}

// TimeRanges maps each bounded TimeScope to its lookback window.
var TimeRanges = map[domain.TimeScope]time.Duration{
	domain.TimeScopeLastWeek:    7 * 24 * time.Hour,
	domain.TimeScopeLastMonth:   30 * 24 * time.Hour,
	domain.TimeScopeLastQuarter: 90 * 24 * time.Hour,
	domain.TimeScopeLastYear:    365 * 24 * time.Hour,
}

// ApplyRecencyWeighting adjusts each hit's score by recency and status.
//
// For each hit, compute adjusted_score = (0.7 × raw + 0.3 × decay) × status_mul
// where decay = 0.5 ^ (age_days / 90) and age_days = math.Floor((now - ts).days).
// Sorts by adjusted_score descending (stable).
//
// BIT-IDENTICAL REQUIREMENT: the reference implementation floors age to whole
// days. Go Hours()/24 is float — must math.Floor to match.
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
		statusMult := 1.0
		if mult, ok := StatusMultiplier[r.Status]; ok {
			statusMult = mult
		}
		r.AdjustedScore = (SimilarityWeight*r.Score + RecencyWeight*decay) * statusMult
	}

	sort.SliceStable(hits, func(i, j int) bool {
		return hits[i].AdjustedScore > hits[j].AdjustedScore
	})
	return hits
}

// FilterByTime drops hits outside the scope's lookback window.
// Records with no timestamp are kept.
func FilterByTime(hits []domain.SearchHit, scope domain.TimeScope, now time.Time) []domain.SearchHit {
	timeRange, ok := TimeRanges[scope]
	if !ok || scope == domain.TimeScopeAllTime {
		return hits
	}

	cutoff := now.Add(-timeRange)
	var filtered []domain.SearchHit

	for i := range hits {
		r := hits[i]
		keep := true
		if tsStr, ok := r.Metadata["timestamp"].(string); ok {
			if ts, err := time.Parse(time.RFC3339, tsStr); err == nil {
				if ts.Before(cutoff) {
					keep = false
				}
			}
		} else if tsFloat, ok := r.Metadata["timestamp"].(float64); ok {
			ts := time.Unix(int64(tsFloat), 0).UTC()
			if ts.Before(cutoff) {
				keep = false
			}
		}

		if keep {
			filtered = append(filtered, r)
		}
	}
	return filtered
}
