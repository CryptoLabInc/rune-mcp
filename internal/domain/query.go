package domain

// Query / recall types + internal search types.

// RecallArgs — recall tool request body.
type RecallArgs struct {
	Query string  `json:"query"`
	TopK  int     `json:"topk,omitempty"`  // default 5; client sanity ceiling 50, real cap from console token role
	Since *string `json:"since,omitempty"` // ISO date "YYYY-MM-DD"
}

// RecallResult — recall tool response body.
type RecallResult struct {
	OK      bool          `json:"ok"`
	Found   int           `json:"found"`
	Results []RecallEntry `json:"results"`
	Error   string        `json:"error,omitempty"`
}

// RecallEntry — one matched record in a RecallResult.
type RecallEntry struct {
	RecordID      string  `json:"record_id"`
	Author        string  `json:"author,omitempty"`
	Insight       string  `json:"insight"`
	Context       string  `json:"context,omitempty"`
	Score         float64 `json:"score"`
	AdjustedScore float64 `json:"adjusted_score"`
}

// SearchHit — one hit in the internal recall search pipeline.
type SearchHit struct {
	RecordID      string
	Author        string
	Insight       string
	Context       string
	Score         float64
	AdjustedScore float64
	Metadata      map[string]any
}
