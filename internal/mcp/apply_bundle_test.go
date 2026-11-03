package mcp_test

import (
	"slices"
	"testing"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
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

	if d.Capture.Dependencies().AgentID != "agent_test" {
		t.Errorf("Capture.AgentID: got %q", d.Capture.Dependencies().AgentID)
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
	d.Capture.SetDependencies(service.CaptureDependencies{AgentID: "preexisting"})

	d.ApplyConsoleBundle(nil)

	if d.Capture.Dependencies().AgentID != "preexisting" {
		t.Errorf("nil bundle should be no-op, but Capture.AgentID changed to %q", d.Capture.Dependencies().AgentID)
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

func TestApplyConsoleBundle_MetadataCapabilityPropagationAndClearing(t *testing.T) {
	d := newDepsForApply()
	for _, tc := range []struct {
		raw  string
		want bool
	}{
		{`{"capabilities":["metadata_envelope_v2"]}`, true},
		{`{"capabilities":["unrelated"]}`, false},
		{`{"capabilities":["metadata_envelope_v2"]}`, true},
		{`{}`, false},
	} {
		b, err := console.ParseManifestJSON(tc.raw)
		if err != nil {
			t.Fatal(err)
		}
		d.ApplyConsoleBundle(b)
		if slices.Contains(d.Capture.Dependencies().Capabilities, "metadata_envelope_v2") != tc.want {
			t.Fatalf("manifest %s: capability=%v want %v", tc.raw, slices.Contains(d.Capture.Dependencies().Capabilities, "metadata_envelope_v2"), tc.want)
		}
	}
}

type bundleConsole struct{ console.Client }

func (*bundleConsole) Close() error { return nil }

type bundleEmbedder struct{ embedder.Client }

func (*bundleEmbedder) Close() error { return nil }

type bundleEncryptor struct{}

func (*bundleEncryptor) EncryptFlat([]float32) ([]byte, error)      { return nil, nil }
func (*bundleEncryptor) EncryptClustered([]float32) ([]byte, error) { return nil, nil }
func (*bundleEncryptor) Close() error                               { return nil }

func TestCaptureBundlePublishesCompleteBoot(t *testing.T) {
	d := newDepsForApply()
	d.Capture.SetDependencies(service.CaptureDependencies{AgentID: "previous"})
	c := &bundleConsole{}
	e := &bundleEmbedder{}
	enc := &bundleEncryptor{}
	d.InjectConsole(c)
	d.InjectEncryptor(enc)
	d.InjectEmbedder(e)
	before := d.Capture.Dependencies()
	if before.AgentID != "previous" || before.Console != nil || before.Embedder != nil || before.Encryptor != nil {
		t.Fatal("capture saw partially injected boot")
	}
	b := &console.Bundle{AgentID: "new", AgentDEK: []byte{7}, Capabilities: []string{"metadata_envelope_v2"}, AuthorDisplayName: "Name"}
	d.ApplyConsoleBundle(b)
	b.AgentDEK[0] = 0
	b.Capabilities[0] = "changed"
	after := d.Capture.Dependencies()
	if after.AgentID != "new" || after.Console != c || after.Embedder != e || after.Encryptor != enc || after.AgentDEK[0] != 7 || after.Capabilities[0] != "metadata_envelope_v2" || after.Author != "Name" {
		t.Fatalf("incomplete or mutable boot snapshot: %+v", after)
	}
}
