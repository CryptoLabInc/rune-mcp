package mcp

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	"github.com/CryptoLabInc/rune-mcp/internal/domain"
	"github.com/CryptoLabInc/rune-mcp/internal/lifecycle"
	"github.com/CryptoLabInc/rune-mcp/internal/service"
)

type leaseConsole struct{ console.Client }

func (*leaseConsole) Insert(_ context.Context, item console.InsertItem) (string, error) {
	return item.ID, nil
}
func (*leaseConsole) Close() error { return nil }

type leaseEmbedder struct {
	embedder.Client
	entered, proceed chan struct{}
}

func (e *leaseEmbedder) EmbedRoute(context.Context, string) (embedder.Routed, error) {
	close(e.entered)
	<-e.proceed
	return embedder.Routed{Vector: []float32{1}, CentroidSetVersion: "v1"}, nil
}
func (*leaseEmbedder) Close() error { return nil }

func TestCaptureRetainsEncryptorUntilPublicationAndOperationDrain(t *testing.T) {
	previous := staleClientCloseTime
	staleClientCloseTime = time.Millisecond
	t.Cleanup(func() { staleClientCloseTime = previous })
	s := service.NewCaptureService()
	d := &Deps{Capture: s}
	first := &fakeEncryptor{}
	second := &fakeEncryptor{}
	e := &leaseEmbedder{entered: make(chan struct{}), proceed: make(chan struct{})}
	d.InjectConsole(&leaseConsole{})
	d.InjectEmbedder(e)
	d.InjectEncryptor(first)
	b := &console.Bundle{AgentID: "a", AgentDEK: bytes.Repeat([]byte{7}, 32), Capabilities: []string{"metadata_envelope_v2"}}
	d.ApplyConsoleBundle(b)
	finished := make(chan error, 1)
	go func() {
		_, err := s.Handle(t.Context(), &domain.CaptureRequest{Insight: "long-running embedding"})
		finished <- err
	}()
	<-e.entered
	// Starting a replacement boot must not retire the still-published generation.
	d.InjectEncryptor(second)
	time.Sleep(20 * time.Millisecond)
	if first.closed.Load() {
		close(e.proceed)
		<-finished
		t.Fatal("encryptor closed before replacement publication")
	}
	d.ApplyConsoleBundle(b)
	time.Sleep(20 * time.Millisecond)
	if first.closed.Load() {
		close(e.proceed)
		<-finished
		t.Fatal("encryptor closed while old capture lease is active")
	}
	close(e.proceed)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !first.closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("drained encryptor was not closed")
		}
		time.Sleep(time.Millisecond)
	}
	if second.closed.Load() {
		t.Fatal("replacement encryptor closed")
	}
}

func TestShutdownDeadlineDoesNotCloseActiveCaptureHandles(t *testing.T) {
	s := service.NewCaptureService()
	d := &Deps{Capture: s, Inflight: lifecycle.NewInflightTracker()}
	enc := &fakeEncryptor{}
	e := &leaseEmbedder{entered: make(chan struct{}), proceed: make(chan struct{})}
	d.InjectConsole(&leaseConsole{})
	d.InjectEmbedder(e)
	d.InjectEncryptor(enc)
	bundle := &console.Bundle{AgentID: "a", AgentDEK: bytes.Repeat([]byte{7}, 32), Capabilities: []string{"metadata_envelope_v2"}}
	d.ApplyConsoleBundle(bundle)
	finished := make(chan error, 1)
	d.Inflight.Begin()
	go func() {
		defer d.Inflight.End()
		_, err := s.Handle(t.Context(), &domain.CaptureRequest{Insight: "capture outlives shutdown deadline"})
		finished <- err
	}()
	<-e.entered
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := d.Shutdown(ctx); err != nil {
		t.Fatal(err)
	}
	if enc.closed.Load() {
		close(e.proceed)
		<-finished
		t.Fatal("shutdown closed a leased native handle")
	}
	late := &fakeEncryptor{}
	d.InjectEncryptor(late)
	d.ApplyConsoleBundle(bundle)
	if !late.closed.Load() {
		t.Error("late boot adapter leaked after shutdown")
	}
	if s.Dependencies().Encryptor != nil {
		t.Error("late boot republished a stopped capture service")
	}
	close(e.proceed)
	if err := <-finished; err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !enc.closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("shutdown did not close the drained handle")
		}
		time.Sleep(time.Millisecond)
	}
}
