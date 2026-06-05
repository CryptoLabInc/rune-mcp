package mcp

import (
	"fmt"
	"strings"

	"github.com/CryptoLabInc/rune-mcp/internal/domain"
	"github.com/CryptoLabInc/rune-mcp/internal/lifecycle"
)

// maxRecallTopK is a client-side sanity ceiling, not the authoritative limit.
// The real per-token cap is enforced by the vault from the token's role
// (rune-admin roles range up to admin's top_k=50). We reject only clearly
// excessive requests here so a valid high-limit token is never falsely blocked;
// a top_k within this ceiling but above the token's role limit is rejected by
// the vault and surfaced as domain.CodeTopKLimit.
const maxRecallTopK = 50

// State gate — called at every tool handler entry.
// Returns appropriate RuneError for non-active states (Python _ensure_pipelines).
//
// Python: server.py:L1503-1518 _ensure_pipelines.
// Recovery hints differ by internal state (rune-mcp.md §에러 처리):
//   - starting            → "Wait 1-2s and retry"
//   - waiting_for_vault   → "Last vault error: {err}. Run /rune:vault_status"
//   - dormant(user)       → "Run /rune:activate"
//   - dormant(vault)      → "Check config.vault.endpoint"
//   - dormant(envector)   → "Check network · API key"
func CheckState(m *lifecycle.Manager) error {
	if m == nil {
		return withHint(domain.ErrPipelineNotReady, "rune-mcp boot has not been wired (Deps.State == nil).")
	}
	switch m.Current() {
	case lifecycle.StateActive:
		return nil
	case lifecycle.StateStarting:
		return withHint(domain.ErrPipelineNotReady, "Rune is starting up. Wait 1-2 seconds and retry.")
	case lifecycle.StateWaitingForVault:
		return withHint(domain.ErrPipelineNotReady, "Waiting for Vault connection. Run /rune:vault_status for diagnostics.")
	case lifecycle.StateDormant:
		return withHint(domain.ErrPipelineNotReady, "Rune is deactivated. Run /rune:activate to re-enable.")
	}
	return domain.ErrInternal
}

func withHint(base *domain.RuneError, hint string) *domain.RuneError {
	return &domain.RuneError{
		Code:         base.Code,
		Message:      base.Message,
		Retryable:    base.Retryable,
		RecoveryHint: hint,
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Input validation (Phase 2 entries)
// ─────────────────────────────────────────────────────────────────────────────

// ValidateCaptureRequest — Python server.py:L1240-1242 (parse_llm_json check).
//   - text empty → ErrInvalidInput
//   - extracted nil → ErrInvalidInput ("Invalid extracted JSON — could not parse")
func ValidateCaptureRequest(req *domain.CaptureRequest) error {
	if strings.TrimSpace(req.Text) == "" {
		return domain.ErrInvalidInput
	}
	if req.Extracted == nil {
		return domain.ErrInvalidInput
	}
	return nil
}

// ValidateRecallArgs — Python server.py:L910-932.
//   - query empty → ErrInvalidInput (D24 early reject)
//   - topk > maxRecallTopK → ErrInvalidInput (sanity ceiling; real limit is the vault's)
//   - topk == 0 → default 5
func ValidateRecallArgs(args *domain.RecallArgs) error {
	if strings.TrimSpace(args.Query) == "" {
		return domain.ErrInvalidInput
	}
	if args.TopK == 0 {
		args.TopK = 5
	}
	if args.TopK > maxRecallTopK {
		return &domain.RuneError{
			Code:    domain.CodeInvalidInput,
			Message: fmt.Sprintf("top_k %d exceeds maximum %d", args.TopK, maxRecallTopK),
		}
	}
	return nil
}

// TruncateTitle — D3. 60-rune truncate (UTF-8 aware).
func TruncateTitle(s string) string {
	runes := []rune(s)
	if len(runes) > domain.MaxTitleLen {
		runes = runes[:domain.MaxTitleLen]
	}
	return string(runes)
}

// ClampConfidence — [0.0, 1.0].
func ClampConfidence(c float64) float64 {
	if c < 0 {
		return 0
	}
	if c > 1 {
		return 1
	}
	return c
}
