package policy

import (
	"testing"
	"time"

	"github.com/CryptoLabInc/rune-mcp/internal/domain"
)

// TestBuildPhases_TitleFromExtractionWhenNoRawText guards the regression where
// batch items (rawEvent.Text == "") with no top-level `title`/`group_title`
// degraded to the generic "General decision" label and a colliding recordID.
// Title derivation must fall back to the extraction's own content
// (reusable_insight, phase content) before the empty raw text.
func TestBuildPhases_TitleFromExtractionWhenNoRawText(t *testing.T) {
	now := time.Date(2026, 6, 5, 12, 0, 0, 0, time.UTC)

	t.Run("reusable_insight-only single item", func(t *testing.T) {
		raw := map[string]any{
			"reusable_insight": "Adopt Linkerd over Istio for the service mesh because it is lighter and matches our scale.",
		}
		rec := buildOne(t, raw, now)
		if rec.Title == "General decision" || rec.Title == "" {
			t.Fatalf("title degraded to %q; want it derived from reusable_insight", rec.Title)
		}
		if got := domain.GenerateRecordID(now, rec.Domain, rec.Title); got == "dec_2026-06-05_general_general_decision" {
			t.Fatalf("recordID collided on generic slug: %q", got)
		}
	})

	t.Run("phase-only multi-phase (no group_title)", func(t *testing.T) {
		raw := map[string]any{
			"phases": []any{
				map[string]any{"phase_title": "Requirements Analysis", "phase_decision": "Need ACID guarantees"},
				map[string]any{"phase_title": "Technology Selection", "phase_decision": "Adopt PostgreSQL"},
			},
		}
		rec := buildOne(t, raw, now)
		if rec.Title == "General decision" || rec.Title == "" {
			t.Fatalf("group title degraded to %q; want it derived from phase content", rec.Title)
		}
	})

	t.Run("explicit title is preserved (control)", func(t *testing.T) {
		raw := map[string]any{"title": "Migrate password hashing to Argon2id"}
		rec := buildOne(t, raw, now)
		if rec.Title != "Migrate password hashing to Argon2id" {
			t.Fatalf("explicit title not preserved: %q", rec.Title)
		}
	})
}

func buildOne(t *testing.T, raw map[string]any, now time.Time) domain.DecisionRecord {
	t.Helper()
	detection, extraction, err := domain.ParseExtractionFromAgent(raw)
	if err != nil {
		t.Fatalf("ParseExtractionFromAgent: %v", err)
	}
	// rawEvent.Text == "" mirrors a batch item: no per-item raw text.
	records, err := BuildPhases(&domain.RawEvent{Source: "test"}, detection, extraction, now)
	if err != nil {
		t.Fatalf("BuildPhases: %v", err)
	}
	if len(records) == 0 {
		t.Fatal("BuildPhases returned 0 records")
	}
	return records[0]
}
