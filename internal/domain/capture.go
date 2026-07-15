package domain

// MCP Capture tool I/O.

// CaptureRequest — capture tool request body.
// Extracted is the flat wire format split into Detection + ExtractionResult
// internally.
type CaptureRequest struct {
	Text      string         `json:"text" jsonschema:"Raw source text the decision was extracted from."`
	Source    string         `json:"source" jsonschema:"Source identifier for the capture (e.g. channel, doc, or session name)."`
	User      string         `json:"user,omitempty" jsonschema:"Optional author of the captured context."`
	Channel   string         `json:"channel,omitempty" jsonschema:"Optional channel the context originated from."`
	Extracted map[string]any `json:"extracted" jsonschema:"FLAT extraction object (the agent-delegated extraction result). Fields: {title, decision, problem, rationale, domain?, status?, tags?[]}. Do not nest these under another key; the object itself is the extraction."`
}

// CaptureResponse — capture tool response body.
// Note: no `similar_to` field. Duplicate record info flows via
// Novelty.Related[] (see NoveltyInfo in query.go).
type CaptureResponse struct {
	OK       bool   `json:"ok"`
	Captured bool   `json:"captured"`
	RecordID string `json:"record_id,omitempty"`
	Title    string `json:"title,omitempty"`
	Domain   Domain `json:"domain,omitempty"`

	Reason  string       `json:"reason,omitempty"`
	Novelty *NoveltyInfo `json:"novelty,omitempty"`

	Error string `json:"error,omitempty"`
}

// RawEvent — input to record_builder.BuildPhases.
type RawEvent struct {
	Text    string
	Source  string
	User    string
	Channel string
}
