package domain_test

// Tests for query/recall domain helpers — the IsReliable / IsPhase
// predicates plus the v0.4-simplified payload extraction.

import (
	"testing"

	"github.com/CryptoLabInc/rune-mcp/internal/domain"
)

// SearchHit.IsReliable — returns true iff certainty is "supported" or
// "partially_supported".
//
// Canonical Certainty values: supported, partially_supported, unsupported,
// unknown.
// Test covers all 4 + empty-string + an unrelated value to lock the
// predicate against accidental broadening (e.g., "high" being added later
// without owner intent).
func TestSearchHit_IsReliable(t *testing.T) {
	cases := []struct {
		name      string
		certainty string
		want      bool
	}{
		{"supported", "supported", true},
		{"partially_supported", "partially_supported", true},
		{"unsupported", "unsupported", false},
		{"unknown", "unknown", false},
		{"empty", "", false},
		{"unrelated_value_high", "high", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &domain.SearchHit{Certainty: tc.certainty}
			if got := h.IsReliable(); got != tc.want {
				t.Errorf("IsReliable(%q) = %v, want %v", tc.certainty, got, tc.want)
			}
		})
	}
}

// SearchHit.IsPhase — returns true iff GroupID is set (pointer presence).
func TestSearchHit_IsPhase(t *testing.T) {
	gid := "grp_2026-01-01_arch_strategy"
	empty := ""

	cases := []struct {
		name    string
		groupID *string
		want    bool
		// note documents intent for surprising cases.
		note string
	}{
		{name: "group_id_nil", groupID: nil, want: false},
		{name: "group_id_set", groupID: &gid, want: true},
		// TODO(yg): confirm with team — should pointer-to-empty-string be
		// treated as "no phase"? Current Go says "phase". Locking in current
		// behavior; flip if the predicate is ever tightened.
		{
			name:    "group_id_pointer_to_empty_string",
			groupID: &empty,
			want:    true,
			note:    "pointer presence drives the predicate; empty string still counts (matches Python `is not None`).",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := &domain.SearchHit{GroupID: tc.groupID}
			if got := h.IsPhase(); got != tc.want {
				t.Errorf("IsPhase() = %v, want %v (%s)", got, tc.want, tc.note)
			}
		})
	}
}

// ExtractPayloadText — strict v2.1 (D32). No v1/v2.0 fallback path.
// This is an INTENTIONAL simplification. Only payload.text is read.
// See domain/query.go:121 for the design comment.
func TestExtractPayloadText(t *testing.T) {
	cases := []struct {
		name string
		meta map[string]any
		want string
	}{
		{
			name: "standard_payload_text",
			meta: map[string]any{"payload": map[string]any{"text": "hello"}},
			want: "hello",
		},
		{
			name: "empty_text_field",
			meta: map[string]any{"payload": map[string]any{"text": ""}},
			want: "",
		},
		{
			name: "payload_missing",
			meta: map[string]any{},
			want: "",
		},
		{
			name: "payload_not_a_map",
			meta: map[string]any{"payload": "raw string"},
			want: "",
		},
		{
			name: "text_field_not_a_string",
			meta: map[string]any{"payload": map[string]any{"text": 42}},
			want: "",
		},
		{
			name: "text_field_missing",
			meta: map[string]any{"payload": map[string]any{"format": "markdown"}},
			want: "",
		},
		{
			name: "nil_metadata",
			meta: nil,
			want: "",
		},
		{
			name: "payload_is_nil",
			meta: map[string]any{"payload": nil},
			want: "",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := domain.ExtractPayloadText(tc.meta)
			if got != tc.want {
				t.Errorf("ExtractPayloadText(%v) = %q, want %q", tc.meta, got, tc.want)
			}
		})
	}
}
