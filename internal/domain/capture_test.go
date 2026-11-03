package domain

import (
	"strings"
	"testing"
)

func TestCaptureContentLimits(t *testing.T) {
	if err := ValidateCaptureContent(strings.Repeat("x", MaxInsightBytes), strings.Repeat("y", MaxContextBytes), strings.Repeat("a", MaxAuthorBytes)); err != nil {
		t.Fatalf("valid maximum record rejected: %v", err)
	}
	for _, tc := range []struct{ name, insight, context, author string }{
		{"author too long", "insight", "", strings.Repeat("x", MaxAuthorBytes+1)},
		{"invalid context UTF8", "insight", string([]byte{0xff}), ""},
		{"invalid author UTF8", "insight", "", string([]byte{0xff})},
		{"empty insight", " \n ", "context", "author"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ValidateCaptureContent(tc.insight, tc.context, tc.author); err == nil {
				t.Fatal("invalid record accepted")
			}
		})
	}
}
