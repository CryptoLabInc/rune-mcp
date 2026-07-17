package mcp

import (
	"fmt"
	"strings"

	"github.com/CryptoLabInc/rune-mcp/internal/domain"
	"github.com/CryptoLabInc/rune-mcp/internal/lifecycle"
)

// maxRecallTopK is a client-side sanity ceiling, not the authoritative limit.
// The real per-token cap is enforced by the console from the token's role
// (rune-console roles range up to admin's top_k=50). We reject only clearly
// excessive requests here so a valid high-limit token is never falsely blocked;
// a top_k within this ceiling but above the token's role limit is rejected by
// the console and surfaced as domain.CodeTopKLimit.
const maxRecallTopK = 50

// State gate — called at every tool handler entry.
// Returns appropriate RuneError for non-active states.
//
// Recovery hints differ by internal state:
//   - starting            → "Wait 1-2s and retry"
//   - waiting_for_console   → "Last console error: {err}. Run /rune:status"
//   - dormant(user)       → "Run /rune:activate"
//   - dormant(console)      → "Check config.console.endpoint"
func CheckState(m *lifecycle.Manager) error {
	if m == nil {
		return withHint(domain.ErrPipelineNotReady, "rune-mcp boot has not been wired (Deps.State == nil).")
	}
	switch m.Current() {
	case lifecycle.StateActive:
		return nil
	case lifecycle.StateStarting:
		return withHint(domain.ErrPipelineNotReady, "Rune is starting up. Wait 1-2 seconds and retry.")
	case lifecycle.StateWaitingForConsole:
		return withHint(domain.ErrPipelineNotReady, "Waiting for Console connection. Run /rune:status for diagnostics.")
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

// ValidateCaptureRequest checks a parsed capture payload.
//   - insight empty → ErrInvalidInput (context is optional)
func ValidateCaptureRequest(req *domain.CaptureRequest) error {
	if strings.TrimSpace(req.Insight) == "" {
		return domain.ErrInvalidInput
	}
	return nil
}

// ValidateRecallArgs checks recall tool arguments.
//   - query empty → ErrInvalidInput (early reject)
//   - topk > maxRecallTopK → ErrInvalidInput (sanity ceiling; real limit is the console's)
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
