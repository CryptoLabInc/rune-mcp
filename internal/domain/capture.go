package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	MaxInsightBytes = 64 * 1024
	MaxContextBytes = 64 * 1024
	MaxAuthorBytes  = 4 * 1024
)

// CaptureRequest — capture tool request body.
type CaptureRequest struct {
	Insight string `json:"insight" jsonschema:"The concise, self-contained knowledge to remember. This is the text that gets embedded and searched on recall — write it so it stands on its own without the surrounding context."`
	Context string `json:"context,omitempty" jsonschema:"The fuller surrounding context for the insight. Stored and returned on recall, but NOT embedded or searched. Use it for supporting detail the insight alone omits."`
}

// CaptureResponse reports successful storage. Novelty is not measured.
type CaptureResponse struct {
	OK       bool   `json:"ok"`
	Captured bool   `json:"captured"`
	RecordID string `json:"record_id,omitempty"`
}

// ValidateCaptureContent mirrors the Console record limits in UTF-8 bytes,
// before any embedding or encryption work begins.
func ValidateCaptureContent(insight, context, author string) error {
	for _, field := range []struct {
		name, value string
		limit       int
	}{
		{"insight", insight, MaxInsightBytes},
		{"context", context, MaxContextBytes},
		{"author", author, MaxAuthorBytes},
	} {
		if !utf8.ValidString(field.value) || len(field.value) > field.limit {
			return &RuneError{Code: CodeInvalidInput, Message: fmt.Sprintf("%s must be valid UTF-8 and at most %d bytes", field.name, field.limit)}
		}
	}
	if strings.TrimSpace(insight) == "" {
		return &RuneError{Code: CodeInvalidInput, Message: "insight must not be empty"}
	}
	return nil
}
