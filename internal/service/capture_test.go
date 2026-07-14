package service

import (
	"context"
	"strings"
	"testing"
)

// TestBatch_RejectsContentlessItems guards the D14 fix: items with no usable
// extraction content are classified as "error" instead of being fabricated into
// a "[batch_capture]" placeholder record (which poisoned the corpus and caused
// cascading false near_duplicate). Rejection is enforced by Handle's shared D14
// guard (empty Text + !extraction.HasContent()), which fires before any adapter
// call, so a zero-value service (nil Embedder/Console) is sufficient.
func TestBatch_RejectsContentlessItems(t *testing.T) {
	s := &CaptureService{}
	items := `[
		{"text": "real prose", "extracted": {"title": "wrapper-shape"}},
		{},
		{"foo": "bar"}
	]`

	res, err := s.Batch(context.Background(), BatchCaptureArgs{Items: items, Source: "test"})
	if err != nil {
		t.Fatalf("Batch returned error: %v", err)
	}
	if res.Total != 3 || res.Errors != 3 || res.Captured != 0 || res.Skipped != 0 {
		t.Fatalf("got total=%d errors=%d captured=%d skipped=%d; want total=3 errors=3 captured=0 skipped=0",
			res.Total, res.Errors, res.Captured, res.Skipped)
	}
	for i, r := range res.Results {
		if r.Status != "error" {
			t.Errorf("item %d: status=%q, want %q", i, r.Status, "error")
		}
		if r.Error == nil || !strings.Contains(*r.Error, "reusable_insight") {
			t.Errorf("item %d: error=%v, want message mentioning reusable_insight", i, r.Error)
		}
	}
}

func TestBatch_InvalidJSON(t *testing.T) {
	s := &CaptureService{}
	if _, err := s.Batch(context.Background(), BatchCaptureArgs{Items: "not json"}); err == nil {
		t.Fatal("expected error for invalid items JSON, got nil")
	}
}
