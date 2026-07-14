package domain

// Capture log entry.
// Format: D20 bit-identical.
// Written to ~/.rune/capture_log.jsonl (0600, append-only).

// CaptureLogEntry — One JSONL line per capture / delete.
// NoveltyScore uses pointer so we can omit when caller doesn't set it.
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
