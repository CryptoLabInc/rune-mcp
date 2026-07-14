package domain

// Novelty classification.

// NoveltyClass — 4 values.
type NoveltyClass string

const (
	NoveltyClassNovel         NoveltyClass = "novel"
	NoveltyClassEvolution     NoveltyClass = "evolution"
	NoveltyClassRelated       NoveltyClass = "related"
	NoveltyClassNearDuplicate NoveltyClass = "near_duplicate"
)

// Score = round(1.0 - max_similarity, 4) — inverted (higher score = more novel).
type NoveltyInfo struct {
	Class   NoveltyClass    `json:"class"`
	Score   float64         `json:"score"`
	Related []RelatedRecord `json:"related"` // top-3 similar records
}

// RelatedRecord — appended by caller.
type RelatedRecord struct {
	ID         string  `json:"id"`
	Title      string  `json:"title"`
	Similarity float64 `json:"similarity"` // round 3
}

// Initial state: {Class: Novel, Score: 1.0, Related: []}.
// Used when no prior records exist (max novelty).
