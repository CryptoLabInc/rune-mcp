package domain

// Query / recall types + internal search types.

// ─────────────────────────────────────────────────────────────────────────────
// QueryIntent (8 values)
// ─────────────────────────────────────────────────────────────────────────────

// QueryIntent — recall query classification (8 values).
type QueryIntent string

const (
	QueryIntentDecisionRationale  QueryIntent = "decision_rationale"
	QueryIntentFeatureHistory     QueryIntent = "feature_history"
	QueryIntentPatternLookup      QueryIntent = "pattern_lookup"
	QueryIntentTechnicalContext   QueryIntent = "technical_context"
	QueryIntentSecurityCompliance QueryIntent = "security_compliance"
	QueryIntentHistoricalContext  QueryIntent = "historical_context"
	QueryIntentAttribution        QueryIntent = "attribution"
	QueryIntentGeneral            QueryIntent = "general"
)

// ─────────────────────────────────────────────────────────────────────────────
// TimeScope (5 values)
// ─────────────────────────────────────────────────────────────────────────────

// TimeScope — recall time-window filter (5 values).
type TimeScope string

const (
	TimeScopeLastWeek    TimeScope = "last_week"    // 7 days
	TimeScopeLastMonth   TimeScope = "last_month"   // 30 days
	TimeScopeLastQuarter TimeScope = "last_quarter" // 90 days
	TimeScopeLastYear    TimeScope = "last_year"    // 365 days
	TimeScopeAllTime     TimeScope = "all_time"     // no filter (default)
)

// ─────────────────────────────────────────────────────────────────────────────
// RecallArgs / RecallResult
// ─────────────────────────────────────────────────────────────────────────────

// RecallArgs — recall tool request body.
type RecallArgs struct {
	Query  string  `json:"query"`
	TopK   int     `json:"topk,omitempty"`   // default 5; client sanity ceiling 50, real cap from console token role
	Domain *string `json:"domain,omitempty"` // filter
	Status *string `json:"status,omitempty"` // filter
	Since  *string `json:"since,omitempty"`  // ISO date "YYYY-MM-DD"
}

// RecallResult — recall tool response body. Synthesized is always false
// (agent-delegated).
type RecallResult struct {
	OK          bool           `json:"ok"`
	Found       int            `json:"found"`
	Results     []RecallEntry  `json:"results"`
	Confidence  float64        `json:"confidence"`
	Sources     []RecallSource `json:"sources"`
	Synthesized bool           `json:"synthesized"` // fixed false
	Error       string         `json:"error,omitempty"`
}

// RecallEntry — one matched record in a RecallResult.
type RecallEntry struct {
	RecordID        string  `json:"record_id"`
	Title           string  `json:"title"`
	Domain          string  `json:"domain"`
	Certainty       string  `json:"certainty"`
	Status          string  `json:"status"`
	Score           float64 `json:"score"`
	AdjustedScore   float64 `json:"adjusted_score"`
	ReusableInsight string  `json:"reusable_insight,omitempty"`
	PayloadText     string  `json:"payload_text,omitempty"`

	GroupID    *string `json:"group_id,omitempty"`
	GroupType  *string `json:"group_type,omitempty"`
	PhaseSeq   *int    `json:"phase_seq,omitempty"`
	PhaseTotal *int    `json:"phase_total,omitempty"`
}

// RecallSource — a record cited as a source in a RecallResult.
type RecallSource struct {
	RecordID string `json:"record_id"`
	Title    string `json:"title"`
}

// ─────────────────────────────────────────────────────────────────────────────
// SearchHit — recall Phase 3-6 internal pipeline
// ─────────────────────────────────────────────────────────────────────────────

// SearchHit — one hit in the internal recall search pipeline.
type SearchHit struct {
	RecordID        string
	Title           string
	PayloadText     string
	Domain          string
	Certainty       string
	Status          string
	Score           float64
	ReusableInsight string
	AdjustedScore   float64
	Metadata        map[string]any

	GroupID    *string
	GroupType  *string
	PhaseSeq   *int
	PhaseTotal *int
}

// IsReliable reports whether the hit's certainty is supported or partially_supported.
func (h *SearchHit) IsReliable() bool {
	return h.Certainty == "supported" || h.Certainty == "partially_supported"
}

// IsPhase reports whether the hit belongs to a record group (phase_chain / bundle).
func (h *SearchHit) IsPhase() bool {
	return h.GroupID != nil
}

// ExtractPayloadText reads payload.text from a record's metadata. Strict v2.1:
// only payload.text is read, with no v1/v2.0 fallback paths.
func ExtractPayloadText(metadata map[string]any) string {
	payload, ok := metadata["payload"].(map[string]any)
	if !ok {
		return ""
	}
	text, _ := payload["text"].(string)
	return text
}

// ─────────────────────────────────────────────────────────────────────────────
// ParsedQuery — recall Phase 2 result
// ─────────────────────────────────────────────────────────────────────────────

// ParsedQuery — recall Phase 2 result.
// No Language field — the agent pre-translates.
type ParsedQuery struct {
	Original        string
	Cleaned         string
	Intent          QueryIntent
	TimeScope       TimeScope
	Entities        []string // max 10
	Keywords        []string // max 15
	ExpandedQueries []string // max 5
}

// ─────────────────────────────────────────────────────────────────────────────
// Detection — capture Phase 2 result
// ─────────────────────────────────────────────────────────────────────────────

// Detection — capture Phase 2 result, built from agent data.
type Detection struct {
	IsSignificant bool    // agent-delegated: always true
	Confidence    float64 // [0.0, 1.0] agent-provided
	Domain        string  // agent-delegated; always "general"
}
