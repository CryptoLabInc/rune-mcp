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
	d.ApplyConsoleBundle(&console.Bundle{AgentID: "agent_test", IndexName: "team-index", KeyID: "key_xyz"})

	if d.Capture.IndexName != "team-index" {
		t.Errorf("Capture.IndexName: got %q", d.Capture.IndexName)
	}
}

func TestApplyConsoleBundle_PropagatesToRecall(t *testing.T) {
	d := newDepsForApply()
	d.ApplyConsoleBundle(&console.Bundle{IndexName: "ix"})

	if d.Recall.IndexName != "ix" {
		t.Errorf("Recall.IndexName: got %q, want ix", d.Recall.IndexName)
	}
}

func TestApplyConsoleBundle_PropagatesToLifecycle(t *testing.T) {
	d := newDepsForApply()
	d.ApplyConsoleBundle(&console.Bundle{IndexName: "ix", KeyID: "key_z"})

	if d.Lifecycle.IndexName != "ix" {
		t.Errorf("Lifecycle.IndexName: got %q", d.Lifecycle.IndexName)
	}
	if d.Lifecycle.KeyID != "key_z" {
		t.Errorf("Lifecycle.KeyID: got %q", d.Lifecycle.KeyID)
	}
}

func TestApplyConsoleBundle_NilBundleNoOp(t *testing.T) {
	d := newDepsForApply()
	d.Capture.IndexName = "preexisting"

	d.ApplyConsoleBundle(nil)

	if d.Capture.IndexName != "preexisting" {
		t.Errorf("nil bundle should be no-op, but Capture.IndexName changed to %q", d.Capture.IndexName)
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
