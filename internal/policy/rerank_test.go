package policy_test

// Tests for ApplyRecencyWeighting + rerank constants.
//
// Black-box style — exercises only the public surface. Internal arithmetic is
// gated through observable AdjustedScore values, computed by hand from the
// documented decay-only formula:
//
//	adjusted = SimilarityWeight × raw + RecencyWeight × decay
//	decay    = 0.5 ^ (ageDays / HalfLifeDays)
//	ageDays  = max(0, floor((now - ts).hours / 24))

import (
	"math"
	"testing"
	"time"

	"github.com/CryptoLabInc/rune-mcp/internal/domain"
	"github.com/CryptoLabInc/rune-mcp/internal/policy"
)

// fixedNow — a deterministic wall clock for all timestamp arithmetic.
var fixedNow = time.Date(2026, 4, 29, 0, 0, 0, 0, time.UTC)

// hitWithTS builds a SearchHit whose metadata contains a "timestamp" field of
// the given concrete type. The two metadata types ApplyRecencyWeighting handles
// are:
//
//	string  — RFC3339
//	float64 — Unix seconds
//
// Anything else (int, nil, missing) leaves ageDays = 0 / decay = 1.0.
func hitWithTS(score float64, tsValue any) domain.SearchHit {
	meta := map[string]any{}
	if tsValue != nil {
		meta["timestamp"] = tsValue
	}
	return domain.SearchHit{
		RecordID: "dec_test",
		Score:    score,
		Metadata: meta,
	}
}

// almostEqual — float comparison with epsilon tight enough to catch a single
// mis-multiplied weight while loose enough to absorb the natural drift of
// 0.5^(integer/90) compounded with 0.7/0.3 weights.
func almostEqual(a, b float64) bool { return math.Abs(a-b) < 1e-9 }

// Constants are part of the public contract — a silent change shifts every
// reranked recall result. Lock the values.
func TestRerankConstants_Locked(t *testing.T) {
	if got := policy.HalfLifeDays; got != 90.0 {
		t.Errorf("HalfLifeDays = %v, want 90.0", got)
	}
	if got := policy.SimilarityWeight; got != 0.7 {
		t.Errorf("SimilarityWeight = %v, want 0.7", got)
	}
	if got := policy.RecencyWeight; got != 0.3 {
		t.Errorf("RecencyWeight = %v, want 0.3", got)
	}
}

// ApplyRecencyWeighting — empty input must not panic and must return an empty
// (or nil) slice.
func TestApplyRecencyWeighting_EmptyInputDoesNotPanic(t *testing.T) {
	got := policy.ApplyRecencyWeighting(nil, fixedNow)
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
	got = policy.ApplyRecencyWeighting([]domain.SearchHit{}, fixedNow)
	if len(got) != 0 {
		t.Errorf("len(got) = %d, want 0", len(got))
	}
}

// ApplyRecencyWeighting — formula gate. Each subtest pins one moving part: the
// timestamp parser branch (string vs float64 vs missing vs invalid), the
// half-life decay, and the math.Floor age semantics.
func TestApplyRecencyWeighting_FormulaGate(t *testing.T) {
	cases := []struct {
		name     string
		hit      domain.SearchHit
		wantAdj  float64
		wantNote string
	}{
		{
			name: "no_timestamp_treats_age_as_zero",
			hit: domain.SearchHit{
				Score:    1.0,
				Metadata: map[string]any{}, // no "timestamp" key
			},
			// decay = 0.5^0 = 1.0; adj = 0.7×1.0 + 0.3×1.0 = 1.0
			wantAdj:  1.0,
			wantNote: "missing-ts path: ageDays = 0",
		},
		{
			name: "nil_metadata_treats_age_as_zero",
			hit: domain.SearchHit{
				Score:    1.0,
				Metadata: nil,
			},
			wantAdj:  1.0,
			wantNote: "nil-metadata path: zero-value read, ageDays = 0",
		},
		{
			name:     "rfc3339_same_day_decay_one",
			hit:      hitWithTS(1.0, fixedNow.Format(time.RFC3339)),
			wantAdj:  1.0,
			wantNote: "RFC3339 path",
		},
		{
			name: "rfc3339_exactly_one_half_life_decay_half",
			hit:  hitWithTS(1.0, fixedNow.Add(-90*24*time.Hour).Format(time.RFC3339)),
			// ageDays = 90 → decay = 0.5; adj = 0.7×1.0 + 0.3×0.5 = 0.85
			wantAdj:  0.85,
			wantNote: "half-life boundary: 90d → decay 0.5",
		},
		{
			name: "rfc3339_two_half_lives_decay_quarter",
			hit:  hitWithTS(1.0, fixedNow.Add(-180*24*time.Hour).Format(time.RFC3339)),
			// ageDays = 180 → decay = 0.25; adj = 0.7×1.0 + 0.3×0.25 = 0.775
			wantAdj:  0.775,
			wantNote: "two half-lives",
		},
		{
			name: "future_timestamp_clamps_age_to_zero",
			hit:  hitWithTS(1.0, fixedNow.Add(48*time.Hour).Format(time.RFC3339)),
			// (now - ts) = -2 days → floor = -2 → max(0, -2) = 0; decay = 1.0
			wantAdj:  1.0,
			wantNote: "future ts: math.Max(0, ...) clamps age to zero",
		},
		{
			name: "partial_day_age_floors_down",
			hit:  hitWithTS(1.0, fixedNow.Add(-36*time.Hour).Format(time.RFC3339)),
			// 1.5 days → floor = 1; decay = 0.5^(1/90)
			wantAdj:  0.7 + 0.3*math.Pow(0.5, 1.0/90.0),
			wantNote: "math.Floor truncates fractional days to whole days",
		},
		{
			name:     "float64_unix_timestamp_path",
			hit:      hitWithTS(1.0, float64(fixedNow.Add(-90*24*time.Hour).Unix())),
			wantAdj:  0.85,
			wantNote: "float64 path: unix timestamp decoded",
		},
		{
			name:     "invalid_rfc3339_string_treats_age_as_zero",
			hit:      hitWithTS(1.0, "not-a-timestamp"),
			wantAdj:  1.0,
			wantNote: "invalid RFC3339: ageDays unchanged",
		},
		{
			name: "int_timestamp_falls_through_to_zero_age",
			hit:  hitWithTS(1.0, int(fixedNow.Add(-90*24*time.Hour).Unix())),
			// The type-switch matches only string and float64; int falls through
			// to ageDays=0 → decay=1.0. In production, JSON-decoded maps yield
			// float64, so int is unreachable on the wire.
			wantAdj:  1.0,
			wantNote: "int falls through to age=0; adj locks 1.0 (not 0.85)",
		},
		{
			name:     "empty_string_timestamp_treats_age_as_zero",
			hit:      hitWithTS(1.0, ""),
			wantAdj:  1.0,
			wantNote: "empty string: parse fails, age stays 0",
		},
		{
			name:     "wrong_type_metadata_treats_age_as_zero",
			hit:      hitWithTS(1.0, []string{"weird"}),
			wantAdj:  1.0,
			wantNote: "wrong type: type-switch fall-through",
		},
		{
			name: "score_half_with_180d_decay_gates_raw_score_scaling",
			hit:  hitWithTS(0.5, fixedNow.Add(-180*24*time.Hour).Format(time.RFC3339)),
			// ageDays = 180 → decay = 0.25; adj = 0.7×0.5 + 0.3×0.25 = 0.425
			wantAdj:  0.425,
			wantNote: "score≠1.0 + decay≠1.0: gates raw-score multiplication",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			hits := []domain.SearchHit{tc.hit}
			policy.ApplyRecencyWeighting(hits, fixedNow)
			got := hits[0].AdjustedScore
			if !almostEqual(got, tc.wantAdj) {
				t.Errorf("AdjustedScore = %v, want %v (%s)", got, tc.wantAdj, tc.wantNote)
			}
		})
	}
}

// sort — descending by AdjustedScore, not raw Score. The case below picks
// timestamps so a "sort by raw Score desc" mutation produces a different order
// than the decay-weighted contract.
func TestApplyRecencyWeighting_SortsDescending(t *testing.T) {
	hits := []domain.SearchHit{
		// (input pos 0) Old, high raw — would be first under sort-by-raw.
		// adj = 0.7×0.99 + 0.3×2^-10 ≈ 0.6932930
		{RecordID: "old_top_raw", Score: 0.99,
			Metadata: map[string]any{"timestamp": fixedNow.Add(-900 * 24 * time.Hour).Format(time.RFC3339)}},
		// (input pos 1) Fresh, medium raw. adj = 0.7×0.50 + 0.3×1.0 = 0.65
		{RecordID: "fresh_med_raw", Score: 0.50,
			Metadata: map[string]any{"timestamp": fixedNow.Format(time.RFC3339)}},
		// (input pos 2) Fresh, identical raw to old_top_raw. adj = 0.993
		{RecordID: "fresh_top_raw", Score: 0.99,
			Metadata: map[string]any{"timestamp": fixedNow.Format(time.RFC3339)}},
	}
	policy.ApplyRecencyWeighting(hits, fixedNow)

	wantOrder := []string{"fresh_top_raw", "old_top_raw", "fresh_med_raw"}
	for i, want := range wantOrder {
		if hits[i].RecordID != want {
			t.Errorf("position %d: got %q, want %q (full order: %v)",
				i, hits[i].RecordID, want, recordIDs(hits))
		}
	}
}

// stable sort — when two hits have identical adjusted_score, their relative
// input order must survive (sort.SliceStable, not sort.Slice).
func TestApplyRecencyWeighting_StableSortPreservesInputOrderOnTies(t *testing.T) {
	mkSame := func(id string) domain.SearchHit {
		return domain.SearchHit{
			RecordID: id, Score: 0.5,
			Metadata: map[string]any{"timestamp": fixedNow.Format(time.RFC3339)},
		}
	}
	hits := []domain.SearchHit{mkSame("first"), mkSame("second"), mkSame("third")}
	policy.ApplyRecencyWeighting(hits, fixedNow)

	for i, want := range []string{"first", "second", "third"} {
		if hits[i].RecordID != want {
			t.Errorf("stable sort lost input order: pos %d got %q, want %q (order: %v)",
				i, hits[i].RecordID, want, recordIDs(hits))
		}
	}
}

// in-place mutation — ApplyRecencyWeighting mutates the slice it was given AND
// returns it (sorts in place, then returns the same slice).
func TestApplyRecencyWeighting_MutatesAndReturnsSameSlice(t *testing.T) {
	hits := []domain.SearchHit{
		hitWithTS(0.5, fixedNow.Format(time.RFC3339)),
		hitWithTS(0.9, fixedNow.Format(time.RFC3339)),
	}
	wantBacking := &hits[0]

	got := policy.ApplyRecencyWeighting(hits, fixedNow)

	if &got[0] != wantBacking {
		t.Errorf("returned slice does not share backing array with input " +
			"— caller relying on in-place mutation breaks")
	}
	if len(got) != len(hits) {
		t.Errorf("len(got)=%d, len(hits)=%d — copy semantics suspected", len(got), len(hits))
	}
	// Expected post-sort order: 0.9 first (adj 0.93), 0.5 second (adj 0.65).
	if hits[0].Score != 0.9 || hits[1].Score != 0.5 {
		t.Errorf("input slice not sorted in place: %v", []float64{hits[0].Score, hits[1].Score})
	}
	if !almostEqual(hits[0].AdjustedScore, 0.93) {
		t.Errorf("AdjustedScore at [0] = %v, want 0.93", hits[0].AdjustedScore)
	}
}

// recordIDs — small helper for cleaner failure messages.
func recordIDs(hits []domain.SearchHit) []string {
	out := make([]string, len(hits))
	for i, h := range hits {
		out[i] = h.RecordID
	}
	return out
}
