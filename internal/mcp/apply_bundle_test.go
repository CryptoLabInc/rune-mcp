package mcp_test

import (
	"testing"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/mcp"
	"github.com/CryptoLabInc/rune-mcp/internal/service"
)

func newDepsForApply() *mcp.Deps {
	return &mcp.Deps{
		Capture:   service.NewCaptureService(),
		Recall:    service.NewRecallService(),
		Lifecycle: service.NewLifecycleService(),
	}
}

func TestApplyConsoleBundle_PropagatesToCapture(t *testing.T) {
	d := newDepsForApply()
	d.ApplyConsoleBundle(&console.Bundle{AgentID: "agent_test", KeyID: "key_xyz"})

	if d.Capture.AgentID != "agent_test" {
		t.Errorf("Capture.AgentID: got %q", d.Capture.AgentID)
	}
}

func TestApplyConsoleBundle_PropagatesToLifecycle(t *testing.T) {
	d := newDepsForApply()
	d.ApplyConsoleBundle(&console.Bundle{KeyID: "key_z"})

	if d.Lifecycle.KeyID != "key_z" {
		t.Errorf("Lifecycle.KeyID: got %q", d.Lifecycle.KeyID)
	}
}

func TestApplyConsoleBundle_NilBundleNoOp(t *testing.T) {
	d := newDepsForApply()
	d.Capture.AgentID = "preexisting"

	d.ApplyConsoleBundle(nil)

	if d.Capture.AgentID != "preexisting" {
		t.Errorf("nil bundle should be no-op, but Capture.AgentID changed to %q", d.Capture.AgentID)
	}
}

func TestApplyConsoleBundle_NilServicesNoOp(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Errorf("nil services panicked: %v", r)
		}
	}()
	d := &mcp.Deps{} // no services
	d.ApplyConsoleBundle(&console.Bundle{AgentID: "x"})
}
