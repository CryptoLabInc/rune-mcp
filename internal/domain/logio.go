package domain

// Capture log entry.
// Written to ~/.rune/capture_log.jsonl (0600, append-only).

// CaptureLogEntry — one JSONL line per capture / delete.
// NoveltyScore uses a pointer so we can omit it when the caller doesn't set it.
type CaptureLogEntry struct {
	TS           string   `json:"ts"`     // RFC3339 UTC
	Action       string   `json:"action"` // "captured" | "deleted"
	ID           string   `json:"id"`
	Title        string   `json:"title"`
	Domain       string   `json:"domain"`
	Mode         string   `json:"mode"` // "standard" | "soft-delete" | ...
	NoveltyClass string   `json:"novelty_class,omitempty"`
	NoveltyScore *float64 `json:"novelty_score,omitempty"`
}
