package mcp_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/CryptoLabInc/rune-mcp/internal/domain"
	"github.com/CryptoLabInc/rune-mcp/internal/lifecycle"
	"github.com/CryptoLabInc/rune-mcp/internal/mcp"
)

// TestCheckState_Gating — only StateActive passes; every other state (including
// the new waiting_for_bootstrap) blocks capture/recall with PIPELINE_NOT_READY.
// waiting_for_bootstrap is the model-download window: kept gated so a capture
// can't route to a backend that isn't up yet, and its hint must point at the
// download, not blame the console.
func TestCheckState_Gating(t *testing.T) {
	cases := []struct {
		state   lifecycle.State
		wantErr bool
		hintHas string // substring the recovery hint must contain (when wantErr)
	}{
		{lifecycle.StateActive, false, ""},
		{lifecycle.StateStarting, true, "starting"},
		{lifecycle.StateWaitingForConsole, true, "Console"},
		{lifecycle.StateWaitingForBootstrap, true, "downloading"},
		{lifecycle.StateDormant, true, "activate"},
	}
	for _, tc := range cases {
		t.Run(tc.state.String(), func(t *testing.T) {
			m := lifecycle.NewManager()
			m.SetState(tc.state)
			err := mcp.CheckState(m)
			if !tc.wantErr {
				if err != nil {
					t.Fatalf("state %v: want nil, got %v", tc.state, err)
				}
				return
			}
			if err == nil {
				t.Fatalf("state %v: want PIPELINE_NOT_READY, got nil", tc.state)
			}
			var re *domain.RuneError
			if !errors.As(err, &re) || re.Code != domain.ErrPipelineNotReady.Code {
				t.Fatalf("state %v: want PIPELINE_NOT_READY, got %v", tc.state, err)
			}
			if tc.hintHas != "" && !strings.Contains(re.RecoveryHint, tc.hintHas) {
				t.Errorf("state %v: hint %q missing %q", tc.state, re.RecoveryHint, tc.hintHas)
			}
		})
	}
}

// TestValidateRecallArgs_TopKCeiling — the client-side cap is a sanity ceiling
// (50), not the authoritative per-token limit. A top_k within the ceiling must
// pass so a high-limit console token is never falsely blocked; only clearly
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
