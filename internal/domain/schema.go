// Package domain holds the core types shared across rune-mcp: the sealed memory
// Record, capture/recall tool I/O, novelty classification, and the error taxonomy.
package domain

import "time"

// Record is the minimal sealed memory record: a timestamped, attributed insight
// (the embedded + searched text) plus its fuller context (sealed, returned on
// recall, NOT embedded).
type Record struct {
	Timestamp time.Time `json:"timestamp"`
	Author    string    `json:"author,omitempty"`
	Insight   string    `json:"insight"`
	Context   string    `json:"context,omitempty"`
}
