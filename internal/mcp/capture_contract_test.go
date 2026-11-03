package mcp_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/CryptoLabInc/rune-mcp/internal/domain"
	"github.com/CryptoLabInc/rune-mcp/internal/lifecycle"
	"github.com/CryptoLabInc/rune-mcp/internal/mcp"
	"github.com/CryptoLabInc/rune-mcp/internal/service"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestCaptureUpgradeErrorAtMCPBoundary(t *testing.T) {
	state := lifecycle.NewManager()
	state.SetState(lifecycle.StateActive)
	deps := &mcp.Deps{State: state, Capture: service.NewCaptureService(), Lifecycle: service.NewLifecycleService()}
	cs := sessionWithDeps(t, deps)
	result, err := cs.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "capture", Arguments: map[string]any{"insight": "private"}})
	if err != nil {
		t.Fatal(err)
	}
	if !result.IsError || len(result.Content) != 1 {
		t.Fatalf("expected tool error: %+v", result)
	}
	var response struct {
		OK    bool             `json:"ok"`
		Error domain.RuneError `json:"error"`
	}
	if err := json.Unmarshal([]byte(result.Content[0].(*sdkmcp.TextContent).Text), &response); err != nil {
		t.Fatal(err)
	}
	if response.OK || response.Error.Code != "CONSOLE_UPGRADE_REQUIRED" || response.Error.Retryable {
		t.Fatalf("unexpected upgrade error: %+v", response)
	}
	if !strings.Contains(response.Error.RecoveryHint, "/rune:deactivate") || !strings.Contains(response.Error.RecoveryHint, "/rune:activate") || !strings.Contains(response.Error.RecoveryHint, "/mcp") {
		t.Fatalf("missing executable recovery hint: %+v", response.Error)
	}
	if strings.Contains(response.Error.Message, "console insert:") {
		t.Fatalf("wrapper leaked into domain error: %s", response.Error.Message)
	}
}

func TestCaptureSchemaDoesNotAdvertiseUnmeasuredNovelty(t *testing.T) {
	cs := newSession(t)
	result, err := cs.ListTools(t.Context(), &sdkmcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range result.Tools {
		if tool.Name != "capture" {
			continue
		}
		raw, err := json.Marshal(tool.OutputSchema)
		if err != nil {
			t.Fatal(err)
		}
		var schema struct {
			Properties map[string]any `json:"properties"`
		}
		if err := json.Unmarshal(raw, &schema); err != nil {
			t.Fatal(err)
		}
		for _, name := range []string{"novelty", "reason", "error"} {
			if _, ok := schema.Properties[name]; ok {
				t.Errorf("obsolete capture field %s is advertised", name)
			}
		}
	}
}

func TestCaptureRequestUTF8ByteLimits(t *testing.T) {
	for _, tc := range []struct{ name, insight, context string }{
		{"insight", strings.Repeat("x", 65537), ""},
		{"context", "valid", strings.Repeat("x", 65537)},
		{"multibyte", strings.Repeat("한", 21846), ""},
		{"invalid UTF8", string([]byte{0xff}), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := mcp.ValidateCaptureRequest(&domain.CaptureRequest{Insight: tc.insight, Context: tc.context}); err == nil {
				t.Fatal("invalid capture input accepted")
			}
		})
	}
}

func TestDiagnosticsReportsNegotiatedMetadataCapability(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	capture := service.NewCaptureService()
	deps := &mcp.Deps{Capture: capture, Lifecycle: service.NewLifecycleService()}
	cs := sessionWithDeps(t, deps)
	for _, capabilities := range [][]string{nil, {"metadata_envelope_v2"}, nil} {
		capture.SetDependencies(service.CaptureDependencies{Capabilities: capabilities})
		result, err := cs.CallTool(t.Context(), &sdkmcp.CallToolParams{Name: "diagnostics"})
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Console struct {
				Capabilities        []string `json:"capabilities"`
				MetadataEnvelopeV2  bool     `json:"metadata_envelope_v2"`
				CaptureRecoveryHint string   `json:"capture_recovery_hint"`
			} `json:"console"`
		}
		if err := json.Unmarshal([]byte(result.Content[0].(*sdkmcp.TextContent).Text), &response); err != nil {
			t.Fatal(err)
		}
		if len(capabilities) > 0 {
			if !response.Console.MetadataEnvelopeV2 || len(response.Console.Capabilities) != 1 {
				t.Fatalf("diagnostics omitted negotiated capture capability: %+v", response)
			}
		} else if response.Console.MetadataEnvelopeV2 || response.Console.CaptureRecoveryHint == "" {
			t.Fatalf("diagnostics omitted blocked capture recovery: %+v", response)
		}
	}
}

func TestToolsForbidRecallAsCaptureDedup(t *testing.T) {
	cs := newSession(t)
	result, err := cs.ListTools(t.Context(), &sdkmcp.ListToolsParams{})
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range result.Tools {
		if tool.Name == "capture" || tool.Name == "recall" {
			text := strings.ToLower(tool.Description)
			if !strings.Contains(text, "recall") || !strings.Contains(text, "plaintext") || !(strings.Contains(text, "never") || strings.Contains(text, "do not")) {
				t.Errorf("%s does not forbid plaintext recall-based dedup", tool.Name)
			}
		}
	}
}
