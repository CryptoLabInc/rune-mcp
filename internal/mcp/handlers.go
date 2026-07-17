package mcp

import (
	"context"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CryptoLabInc/rune-mcp/internal/domain"
	"github.com/CryptoLabInc/rune-mcp/internal/service"
)

// ─────────────────────────────────────────────────────────────────────────────
// Write tools — gated by CheckState (PIPELINE_NOT_READY when not active).
// ─────────────────────────────────────────────────────────────────────────────

func handleCapture(deps *Deps) sdkmcp.ToolHandlerFor[domain.CaptureRequest, domain.CaptureResponse] {
	return func(ctx context.Context, _ *sdkmcp.CallToolRequest, in domain.CaptureRequest) (*sdkmcp.CallToolResult, domain.CaptureResponse, error) {
		var zero domain.CaptureResponse
		if err := CheckState(deps.State); err != nil {
			return errorResult(err), zero, nil
		}
		if err := ValidateCaptureRequest(&in); err != nil {
			return errorResult(err), zero, nil
		}
		out, err := deps.Capture.Handle(ctx, &in)
		if err != nil {
			return errorResult(err), zero, nil
		}
		return okResult(out), *out, nil
	}
}

func handleRecall(deps *Deps) sdkmcp.ToolHandlerFor[domain.RecallArgs, domain.RecallResult] {
	return func(ctx context.Context, _ *sdkmcp.CallToolRequest, in domain.RecallArgs) (*sdkmcp.CallToolResult, domain.RecallResult, error) {
		var zero domain.RecallResult
		if err := CheckState(deps.State); err != nil {
			return errorResult(err), zero, nil
		}
		if err := ValidateRecallArgs(&in); err != nil {
			return errorResult(err), zero, nil
		}
		out, err := deps.Recall.Handle(ctx, &in)
		if err != nil {
			return errorResult(err), zero, nil
		}
		return okResult(out), *out, nil
	}
}

// ─────────────────────────────────────────────────────────────────────────────
// Read / diagnostic tools — bypass CheckState. Surface partial state to the
// agent (console_status, diagnostics) so users can troubleshoot pre-active.
// ─────────────────────────────────────────────────────────────────────────────

func handleConsoleStatus(deps *Deps) sdkmcp.ToolHandlerFor[emptyArgs, service.ConsoleStatusResult] {
	return func(ctx context.Context, _ *sdkmcp.CallToolRequest, _ emptyArgs) (*sdkmcp.CallToolResult, service.ConsoleStatusResult, error) {
		var zero service.ConsoleStatusResult
		out, err := deps.Lifecycle.ConsoleStatus(ctx)
		if err != nil {
			return errorResult(err), zero, nil
		}
		return okResult(out), *out, nil
	}
}

func handleDiagnostics(deps *Deps) sdkmcp.ToolHandlerFor[emptyArgs, service.DiagnosticsResult] {
	return func(ctx context.Context, _ *sdkmcp.CallToolRequest, _ emptyArgs) (*sdkmcp.CallToolResult, service.DiagnosticsResult, error) {
		out := deps.Lifecycle.Diagnostics(ctx)
		return okResult(out), *out, nil
	}
}

func handleConfigure(deps *Deps) sdkmcp.ToolHandlerFor[service.ConfigureArgs, service.ConfigureResult] {
	return func(ctx context.Context, _ *sdkmcp.CallToolRequest, in service.ConfigureArgs) (*sdkmcp.CallToolResult, service.ConfigureResult, error) {
		var zero service.ConfigureResult
		out, err := deps.Lifecycle.Configure(ctx, in) // write Console credentials to $HOME/.rune/config.json
		if err != nil {
			return errorResult(err), zero, nil
		}
		return okResult(out), *out, nil
	}
}

func handleActivate(deps *Deps) sdkmcp.ToolHandlerFor[emptyArgs, service.ActivateResult] {
	return func(ctx context.Context, _ *sdkmcp.CallToolRequest, _ emptyArgs) (*sdkmcp.CallToolResult, service.ActivateResult, error) {
		var zero service.ActivateResult
		out, err := deps.Lifecycle.Activate(ctx)
		if err != nil {
			return errorResult(err), zero, nil
		}
		return okResult(out), *out, nil
	}
}

func handleDeactivate(deps *Deps) sdkmcp.ToolHandlerFor[emptyArgs, service.DeactivateResult] {
	return func(ctx context.Context, _ *sdkmcp.CallToolRequest, _ emptyArgs) (*sdkmcp.CallToolResult, service.DeactivateResult, error) {
		var zero service.DeactivateResult
		out, err := deps.Lifecycle.Deactivate(ctx)
		if err != nil {
			return errorResult(err), zero, nil
		}
		return okResult(out), *out, nil
	}
}
