package mcp_test

import (
	"errors"
	"testing"

	"github.com/CryptoLabInc/rune-mcp/internal/domain"
	"github.com/CryptoLabInc/rune-mcp/internal/mcp"
)

// TestValidateRecallArgs_TopKCeiling — the client-side cap is a sanity ceiling
// (50), not the authoritative per-token limit. A top_k within the ceiling must
// pass so a high-limit vault token is never falsely blocked; only clearly
// excessive values are rejected with INVALID_INPUT.
func TestValidateRecallArgs_TopKCeiling(t *testing.T) {
	cases := []struct {
		name    string
		topk    int
		wantErr bool
	}{
		{"within legacy limit", 10, false},
		{"above legacy limit but valid for admin token", 50, false},
		{"just over ceiling", 51, true},
		{"absurd", 10000, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := &domain.RecallArgs{Query: "q", TopK: tc.topk}
			err := mcp.ValidateRecallArgs(args)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("topk=%d: expected error, got nil", tc.topk)
				}
				var re *domain.RuneError
				if !errors.As(err, &re) || re.Code != domain.CodeInvalidInput {
					t.Errorf("topk=%d: want INVALID_INPUT, got %v", tc.topk, err)
				}
			} else if err != nil {
				t.Errorf("topk=%d: unexpected error %v", tc.topk, err)
			}
		})
	}
}

// TestValidateRecallArgs_DefaultsAndEmpty — topk 0 defaults to 5; empty query
// is rejected.
func TestValidateRecallArgs_DefaultsAndEmpty(t *testing.T) {
	args := &domain.RecallArgs{Query: "q", TopK: 0}
	if err := mcp.ValidateRecallArgs(args); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if args.TopK != 5 {
		t.Errorf("default topk: got %d, want 5", args.TopK)
	}

	if err := mcp.ValidateRecallArgs(&domain.RecallArgs{Query: "   ", TopK: 3}); err == nil {
		t.Error("blank query: expected error, got nil")
	}
}
