package service

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	"github.com/CryptoLabInc/rune-mcp/internal/domain"
)

type switchingEmbedder struct {
	*resyncEmbedder
	duringRoute func()
}

func (e *switchingEmbedder) EmbedRoute(ctx context.Context, text string) (embedder.Routed, error) {
	if e.duringRoute != nil {
		e.duringRoute()
	}
	return e.resyncEmbedder.EmbedRoute(ctx, text)
}

type switchingConsole struct {
	*privacyConsole
	afterInsert func()
}

func (c *switchingConsole) Insert(ctx context.Context, item console.InsertItem) (string, error) {
	id, err := c.privacyConsole.Insert(ctx, item)
	if c.afterInsert != nil {
		c.afterInsert()
	}
	return id, err
}

func TestCaptureKeepsOneBootSnapshot(t *testing.T) {
	a := &privacyConsole{resyncConsole: &resyncConsole{engineVersion: "v2"}}
	b := &privacyConsole{resyncConsole: &resyncConsole{engineVersion: "v2"}}
	e := &switchingEmbedder{resyncEmbedder: &resyncEmbedder{version: "v2"}}
	s := newResyncService(e, a.resyncConsole)
	d := s.Dependencies()
	d.Console = a
	s.SetDependencies(d)
	s.Now = time.Now
	e.duringRoute = func() {
		d.Console = b
		d.AgentID = "next-agent"
		d.AgentDEK = bytes.Repeat([]byte{9}, 32)
		s.SetDependencies(d)
	}
	result, err := s.Handle(t.Context(), &domain.CaptureRequest{Insight: "one boot identity"})
	if err != nil {
		t.Fatal(err)
	}
	if len(a.items) != 1 || len(b.items) != 0 {
		t.Fatalf("in-flight capture crossed boot snapshots: old inserts=%d new inserts=%d", len(a.items), len(b.items))
	}
	var envelope struct {
		A string `json:"a"`
		R string `json:"r"`
	}
	if err := json.Unmarshal([]byte(a.items[0].SealedMetadata), &envelope); err != nil {
		t.Fatal(err)
	}
	if envelope.A != "agent-test" || envelope.R != result.RecordID {
		t.Fatalf("mixed identity: %+v", envelope)
	}
}

func TestCaptureRebuildRechecksCapability(t *testing.T) {
	e := &resyncEmbedder{version: "v1"}
	c := &switchingConsole{privacyConsole: &privacyConsole{resyncConsole: &resyncConsole{engineVersion: "v2"}}}
	s := newResyncService(e, c.resyncConsole)
	d := s.Dependencies()
	d.Console = c
	s.SetDependencies(d)
	c.afterInsert = func() { d.Capabilities = nil; s.SetDependencies(d) }
	_, err := s.EncryptSealInsert(t.Context(), "private", `{}`)
	if err == nil {
		t.Fatal("rebuild continued after capability was revoked")
	}
	if e.routeCalls != 1 || e.pushCalls != 0 || len(c.items) != 1 {
		t.Fatalf("revoked capability still used adapters: routes=%d pushes=%d inserts=%d", e.routeCalls, e.pushCalls, len(c.items))
	}
}

func TestCaptureConcurrentBootReplacement(t *testing.T) {
	a := &privacyConsole{resyncConsole: &resyncConsole{engineVersion: "v2"}}
	b := &privacyConsole{resyncConsole: &resyncConsole{engineVersion: "v2"}}
	e := &resyncEmbedder{version: "v2"}
	s := newResyncService(e, a.resyncConsole)
	first := s.Dependencies()
	first.Console = a
	second := first
	second.Console = b
	second.AgentID = "next-agent"
	second.AgentDEK = bytes.Repeat([]byte{9}, 32)
	s.SetDependencies(first)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for range 1000 {
			s.SetDependencies(second)
			s.SetDependencies(first)
		}
	}()
	for range 100 {
		if _, err := s.Handle(t.Context(), &domain.CaptureRequest{Insight: "concurrent boot capture"}); err != nil {
			t.Fatal(err)
		}
	}
	<-done
	for _, tc := range []struct {
		client *privacyConsole
		agent  string
	}{{a, "agent-test"}, {b, "next-agent"}} {
		for _, item := range tc.client.items {
			var env struct {
				A string `json:"a"`
			}
			if err := json.Unmarshal([]byte(item.SealedMetadata), &env); err != nil {
				t.Fatal(err)
			}
			if env.A != tc.agent {
				t.Fatalf("identity %s sent to %s client", env.A, tc.agent)
			}
		}
	}
}

func TestCaptureOwnsInstalledSlices(t *testing.T) {
	s := NewCaptureService()
	d := CaptureDependencies{AgentDEK: bytes.Repeat([]byte{7}, 32), Capabilities: []string{"metadata_envelope_v2"}}
	s.SetDependencies(d)
	d.AgentDEK[0] = 0
	d.Capabilities[0] = "revoked"
	got := s.Dependencies()
	if got.AgentDEK[0] != 7 || got.Capabilities[0] != "metadata_envelope_v2" {
		t.Fatal("caller mutation changed installed capture dependencies")
	}
	got.AgentDEK[0] = 1
	got.Capabilities[0] = "changed"
	if next := s.Dependencies(); next.AgentDEK[0] != 7 || next.Capabilities[0] != "metadata_envelope_v2" {
		t.Fatal("snapshot getter exposed mutable installed slices")
	}
}

func TestCaptureRebuildDoesNotCrossBootDestination(t *testing.T) {
	e := &resyncEmbedder{version: "v1"}
	first := &switchingConsole{privacyConsole: &privacyConsole{resyncConsole: &resyncConsole{engineVersion: "v2"}}}
	next := &privacyConsole{resyncConsole: &resyncConsole{engineVersion: "v2"}}
	s := newResyncService(e, first.resyncConsole)
	d := s.Dependencies()
	d.Console = first
	s.SetDependencies(d)
	first.afterInsert = func() {
		d.Console = next
		d.AgentID = "other-workspace-agent"
		d.AgentDEK = bytes.Repeat([]byte{9}, 32)
		s.SetDependencies(d)
	}
	_, err := s.Handle(t.Context(), &domain.CaptureRequest{Insight: "belongs only in original workspace"})
	if err == nil {
		t.Fatal("capture rerouted to replacement boot destination")
	}
	if len(next.items) != 0 || e.pushCalls != 0 || e.routeCalls != 1 {
		t.Fatalf("replacement used private content: inserts=%d routes=%d pushes=%d", len(next.items), e.routeCalls, e.pushCalls)
	}
}
