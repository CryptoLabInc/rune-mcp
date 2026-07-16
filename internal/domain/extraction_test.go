package domain_test

import (
	"testing"

	"github.com/CryptoLabInc/rune-mcp/internal/domain"
)

// TestExtractionHasContent drives HasContent through ParseExtractionFromAgent —
// the real path the capture pipeline takes — so it asserts the content gate agrees
// with what BuildPhases/RenderPayloadText actually embed.
//
// Regressions guarded:
//   - A {group_title, phases} multi-phase item and a phase-only item carry no
//     top-level title/reusable_insight yet embed fine in single capture; they
//     MUST be accepted (the old top-level-string gate falsely rejected them).
//   - The {text, extracted} wrapper anti-pattern and a bare {} carry nothing the
//     parser can see; they MUST be rejected (no placeholder laundering).
func TestExtractionHasContent(t *testing.T) {
	cases := []struct {
		name string
		item map[string]any
		want bool
	}{
		{"reusable_insight", map[string]any{"reusable_insight": "ri"}, true},
		{"title", map[string]any{"title": "t"}, true},
		{"rationale only (no title)", map[string]any{"rationale": "because X"}, true},
		{"problem only (no title)", map[string]any{"problem": "P"}, true},
		// {group_title, phases} multi-phase shape — accepted.
		{"multi-phase with group_title", map[string]any{
			"group_title": "g",
			"phases": []any{
				map[string]any{"phase_title": "A", "phase_decision": "do A"},
				map[string]any{"phase_title": "B", "phase_decision": "do B"},
			},
		}, true},
		// Multi-phase WITHOUT any top-level identifier — content lives in phases;
		// single capture embeds it, so HasContent must be true.
		{"multi-phase no group_title", map[string]any{
			"phases": []any{
				map[string]any{"phase_title": "A", "phase_decision": "do A"},
				map[string]any{"phase_title": "B", "phase_rationale": "why B"},
			},
		}, true},
		// Single-phase item whose content sits under phase_title — accepted.
		{"single-phase via phase_title", map[string]any{
			"phases": []any{map[string]any{"phase_title": "X", "phase_rationale": "why"}},
		}, true},
		// Rejected: nothing the parser can see.
		{"empty item", map[string]any{}, false},
		{"unknown keys only", map[string]any{"foo": "bar", "decision": "d"}, false},
		// Degenerate: group_title with no phases. The single-record branch of
		// ParseExtractionFromAgent ignores group_title (it is only read in the
		// multi-phase branch), so the parsed extraction is empty and the item is
		// rejected. The new gate matches the parser exactly here — the old
		// top-level-string gate would have wrongly accepted this label-only item.
		{"group_title without phases", map[string]any{"group_title": "g"}, false},
		// {text, extracted} wrapper — fields nest under "extracted", invisible to
		// the top-level lookup, so the parsed extraction is empty.
		{"wrapper shape", map[string]any{
			"text":      "real prose",
			"extracted": map[string]any{"title": "t", "reusable_insight": "ri"},
		}, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, ext, err := domain.ParseExtractionFromAgent(c.item)
			if err != nil {
				t.Fatalf("ParseExtractionFromAgent(%v) error: %v", c.item, err)
			}
			if got := ext.HasContent(); got != c.want {
				t.Fatalf("HasContent() = %v, want %v (parsed: %+v)", got, c.want, ext)
			}
		})
	}
}
