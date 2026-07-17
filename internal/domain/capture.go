package domain

// MCP Capture tool I/O.

// CaptureRequest — capture tool request body.
type CaptureRequest struct {
	Insight string `json:"insight" jsonschema:"The concise, self-contained knowledge to remember. This is the text that gets embedded and searched on recall — write it so it stands on its own without the surrounding context."`
	Context string `json:"context,omitempty" jsonschema:"The fuller surrounding context for the insight. Stored and returned on recall, but NOT embedded or searched. Use it for supporting detail the insight alone omits."`
}

// CaptureResponse — capture tool response body.
// Note: duplicate record info flows via Novelty.Related[] (see NoveltyInfo in
// query.go).
type CaptureResponse struct {
	OK       bool         `json:"ok"`
	Captured bool         `json:"captured"`
	RecordID string       `json:"record_id,omitempty"`
	Reason   string       `json:"reason,omitempty"`
	Novelty  *NoveltyInfo `json:"novelty,omitempty"`
	Error    string       `json:"error,omitempty"`
}
