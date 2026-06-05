package service

import (
	"errors"
	"fmt"
	"testing"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/vault"
	"github.com/CryptoLabInc/rune-mcp/internal/domain"
)

// TestTopKLimitErr — a wrapped vault top_k rejection is converted to a distinct
// domain.CodeTopKLimit error so recall aborts and surfaces it instead of
// swallowing it best-effort (which would yield silent zero results). Other
// errors return nil so they keep their best-effort handling.
func TestTopKLimitErr(t *testing.T) {
	topk := &vault.Error{Code: vault.ErrVaultTopKExceeded.Code, Message: "top_k 8 exceeds limit 3 for role 'researcher'"}

	cases := []struct {
		name     string
		err      error
		wantTopK bool
	}{
		{"direct vault topk error", topk, true},
		{"wrapped vault topk error", fmt.Errorf("vault decrypt scores: %w", topk), true},
		{"generic invalid input", &vault.Error{Code: vault.ErrVaultInvalidInput.Code}, false},
		{"unavailable", &vault.Error{Code: vault.ErrVaultUnavailable.Code}, false},
		{"non-vault error", errors.New("boom"), false},
		{"nil", nil, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := topKLimitErr(tc.err)
			if tc.wantTopK {
				if got == nil {
					t.Fatal("expected TOPK_LIMIT error, got nil")
				}
				if got.Code != domain.CodeTopKLimit {
					t.Errorf("Code: got %q, want %q", got.Code, domain.CodeTopKLimit)
				}
				if got.Retryable {
					t.Error("TOPK_LIMIT must not be retryable")
				}
				if got.Message != topk.Message {
					t.Errorf("Message: got %q, want %q", got.Message, topk.Message)
				}
			} else if got != nil {
				t.Errorf("expected nil, got %v", got)
			}
		})
	}
}
