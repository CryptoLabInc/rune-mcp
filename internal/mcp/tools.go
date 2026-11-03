// Package mcp wires the MCP tool handlers onto the official Go SDK and owns
// Deps injection + state-aware response shaping.
//
// SDK: github.com/modelcontextprotocol/go-sdk v1.5.0+. Stdio transport.
// Input schema is auto-inferred from the Go input struct (jsonschema tags
// optional but recommended).
//
// Each handler runs CheckState then dispatches to a service-layer call
// (CheckState → service.X.Handle → response wrap). Write tools fail with
// PIPELINE_NOT_READY until the boot loop populates adapter clients; read-only
// tools (diagnostics) bypass the state gate.
// The registered set lives in Register.
package mcp

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	"github.com/CryptoLabInc/rune-mcp/internal/lifecycle"
	"github.com/CryptoLabInc/rune-mcp/internal/service"
)

// Deps — injected into every MCP handler.
//
// State + 3 services drive request handling. cmd/rune-mcp/main.go constructs
// Deps after the boot loop has populated adapter clients on the services.
// Until boot completes, write tools fail with PIPELINE_NOT_READY through
// CheckState; read-only tools (diagnostics) can run pre-active.
type Deps struct {
	adapterMu sync.Mutex
	closed    bool
	Console   console.Client
	Embedder  embedder.Client
	Encryptor lifecycle.Encryptor
	State     *lifecycle.Manager

	Capture   *service.CaptureService
	Recall    *service.RecallService
	Lifecycle *service.LifecycleService

	// Inflight counts active tool invocations so the exit sequence can drain
	// them (GracefulShutdown step 1) instead of cutting a capture mid-insert.
	// Every registered handler passes through it via mustAdd.
	Inflight *lifecycle.InflightTracker
}

// Retain a grace period for existing recall/diagnostic calls after capture
// generations drain. Capture lifetime itself is protected by leases.
var staleClientCloseTime = 30 * time.Second

func (d *Deps) InjectConsole(client console.Client) {
	d.adapterMu.Lock()
	defer d.adapterMu.Unlock()
	if d.closed {
		if client != nil {
			_ = (&captureProtectedCloser{capture: d.Capture, resource: client}).Close()
		}
		return
	}
	prev := d.Console
	d.Console = client
	if d.Recall != nil {
		d.Recall.Console = client
	}
	if d.Lifecycle != nil {
		d.Lifecycle.Console = client
	}
	d.closeAfterInterval("console", prev, client)
}

func (d *Deps) InjectEncryptor(enc lifecycle.Encryptor) {
	d.adapterMu.Lock()
	defer d.adapterMu.Unlock()
	if d.closed {
		if enc != nil {
			_ = (&captureProtectedCloser{capture: d.Capture, resource: enc}).Close()
		}
		return
	}
	prev := d.Encryptor
	d.Encryptor = enc
	// The replaced encryptor wraps a cgo (libevi) key context the GC cannot
	// reclaim — drain in-flight captures, then Close it, same as the console /
	// embedder injectors above. Without this every re-boot cycle (console
	// restart, boot retry, reload_pipelines) leaks one native key context.
	d.closeAfterInterval("encryptor", prev, enc)
}

func (d *Deps) InjectEmbedder(client embedder.Client) {
	d.adapterMu.Lock()
	defer d.adapterMu.Unlock()
	if d.closed {
		if client != nil {
			_ = (&captureProtectedCloser{capture: d.Capture, resource: client}).Close()
		}
		return
	}
	prev := d.Embedder
	d.Embedder = client
	if d.Recall != nil {
		d.Recall.Embedder = client
	}
	if d.Lifecycle != nil {
		d.Lifecycle.SetEmbedder(client)
	}
	d.closeAfterInterval("embedder", prev, client)
}

// Defer retirement until no capture generation can use the replaced adapter.
// The fallback grace period covers the existing recall/diagnostic lifetimes.
func (d *Deps) closeAfterInterval(name string, prev, next io.Closer) {
	if prev == nil || prev == next {
		return
	}
	delay := staleClientCloseTime
	retire := func() {
		go func() {
			time.Sleep(delay)
			if err := prev.Close(); err != nil {
				slog.Warn("close replaced adapter client", "name", name, "err", err)
			}
		}()
	}
	if d.Capture != nil {
		d.Capture.AfterCaptureDrain(prev, retire)
	} else {
		retire()
	}
}

// Shutdown excludes late boot injection, unpublishes capture generations, and
// drains tool calls. A timed-out capture still owns its native handles until it
// returns; its adapter closure and DEK clearing remain deferred by the lease.
func (d *Deps) Shutdown(ctx context.Context) error {
	d.adapterMu.Lock()
	if d.closed {
		d.adapterMu.Unlock()
		return nil
	}
	d.closed = true
	resources := []io.Closer{d.Encryptor, d.Console, d.Embedder}
	d.adapterMu.Unlock()
	if d.Capture != nil {
		d.Capture.Shutdown()
	}
	closers := []lifecycle.Closer{}
	for _, resource := range resources {
		if resource != nil {
			closers = append(closers, &captureProtectedCloser{capture: d.Capture, resource: resource})
		}
	}
	if d.State != nil {
		closers = append(closers, d.State.BootLog())
	}
	return lifecycle.GracefulShutdown(ctx, d.Inflight, closers)
}

type captureProtectedCloser struct {
	capture  *service.CaptureService
	resource io.Closer
}

func (c *captureProtectedCloser) Close() error {
	if c.capture == nil {
		return c.resource.Close()
	}
	c.capture.AfterCaptureDrain(c.resource, func() {
		if err := c.resource.Close(); err != nil {
			slog.Warn("close drained capture adapter", "err", err)
		}
	})
	return nil
}

// ApplyConsoleBundle propagates agent identity, per-agent metadata DEK and
// negotiated capabilities to capture, and KeyID to lifecycle. The boot loop
// calls it after Console.GetAgentManifest. The manifest also supplies public
// EncKeys for client-side capture encryption; Console retains HE secret keys
// and decrypts authorized scores and content. Recall queries remain plaintext.
func (d *Deps) ApplyConsoleBundle(b *console.Bundle) {
	d.adapterMu.Lock()
	defer d.adapterMu.Unlock()
	if d.closed {
		return
	}
	if b == nil {
		return
	}
	if d.Capture != nil {
		d.Capture.SetDependencies(service.CaptureDependencies{
			Console: d.Console, Embedder: d.Embedder, Encryptor: d.Encryptor,
			AgentID: b.AgentID, AgentDEK: b.AgentDEK, Capabilities: b.Capabilities,
			Author: formatAuthor(b.AuthorDisplayName, b.AuthorEmail),
		})
	}
	if d.Lifecycle != nil {
		d.Lifecycle.KeyID = b.KeyID
	}
}

// formatAuthor renders record attribution: "Display Name <email>" when
// both are present, else the nonempty display name or email, else "".
func formatAuthor(displayName, email string) string {
	if displayName != "" && email != "" {
		return fmt.Sprintf("%s <%s>", displayName, email)
	}
	if displayName != "" {
		return displayName
	}
	return email
}

// emptyArgs — input type for tools that take no arguments.
type emptyArgs struct{}

// Register binds the MCP tools onto the provided SDK server.
//
// Tool names are a stable wire contract. SDK sorts tools alphabetically in
// `tools/list` output, so order here is for readability.
//
// Failure modes that Register surfaces as a startup error (via panic +
// recover):
//  1. mustAdd name validation (the SDK only logs on a bad name; we panic).
//  2. SDK schema-inference panic (toolForErr).
//  3. SDK schema-shape panic (Server.AddTool).
//
// Result: every registration either succeeds completely or returns an error.
// No silent half-registrations.
func Register(srv *sdkmcp.Server, deps *Deps) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("mcp.Register: %v", r)
		}
	}()

	// Write tools — state-gated.
	mustAdd(srv, deps.Inflight, "capture",
		"Use to remember an insight worth recalling later. Provide `insight` (the concise, self-contained knowledge that gets embedded and searched) and optionally `context` (fuller surrounding detail, stored and returned but not searched). Capture does not check for similar stored records; separate calls can store duplicate insights, so submit each decision once. Never call recall with the proposed insight to check duplicates: recall sends its query embedding in plaintext and would expose the capture embedding.",
		handleCapture(deps))
	mustAdd(srv, deps.Inflight, "recall",
		"Use to retrieve previously captured insights relevant to a natural-language `query`. Returns the most similar records, recency-weighted. Do not use recall(insight) as a duplicate check before capture; recall sends a plaintext query embedding.",
		handleRecall(deps))
	// Read / diagnostic tools — bypass state gate.
	mustAdd(srv, deps.Inflight, "diagnostics",
		"Collect a 5-section health snapshot (env / state / console / keys / embedding).",
		handleDiagnostics(deps))
	mustAdd(srv, deps.Inflight, "configure",
		"Use to connect Rune to its Console: first-time setup, or reconnecting with a new invite. Pass the registration_string (the runev1_… string from the invite email) — the server bootstraps the endpoint, token, and pinned CA from it.",
		handleConfigure(deps))
	mustAdd(srv, deps.Inflight, "activate",
		"Use to bring Rune online: resume after deactivate, or finish boot after configure. Returns a status (configure_required / install_pending / waiting_for_bootstrap / active / dormant), each with a hint for the next step.",
		handleActivate(deps))
	mustAdd(srv, deps.Inflight, "deactivate",
		"Use when the user wants to pause Rune — stop capture/recall for now without losing their setup. Flips active→dormant; resume with activate.",
		handleDeactivate(deps))

	return nil
}

// mustAdd wraps sdkmcp.AddTool with up-front name validation and inflight
// tracking.
//
// The SDK's Server.AddTool only LOGS on invalid tool names — it does not
// panic, so Register's defer recover() would miss it and the bad-named tool
// would silently register. mustAdd panics on invalid names, unifying the
// failure path so recover() catches everything.
//
// Every handler is wrapped with tracker Begin/End so the exit sequence can
// drain in-flight calls (GracefulShutdown step 1). A nil tracker skips the
// wrap (tests that register without one).
func mustAdd[In, Out any](srv *sdkmcp.Server, tracker *lifecycle.InflightTracker, name, description string, h sdkmcp.ToolHandlerFor[In, Out]) {
	if !isValidToolName(name) {
		panic(fmt.Errorf("mustAdd: invalid tool name %q (allowed: [A-Za-z0-9_-], 1..128 chars)", name))
	}
	wrapped := h
	if tracker != nil {
		wrapped = func(ctx context.Context, req *sdkmcp.CallToolRequest, in In) (*sdkmcp.CallToolResult, Out, error) {
			tracker.Begin()
			defer tracker.End()
			return h(ctx, req, in)
		}
	}
	sdkmcp.AddTool(srv, &sdkmcp.Tool{
		Name:        name,
		Description: description,
	}, wrapped)
}

// isValidToolName mirrors the SDK's validateToolName rules: non-empty,
// ≤128 chars, only [A-Za-z0-9_-]. Update this when bumping the SDK if its
// validation tightens.
func isValidToolName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || r == '_' || r == '-'
		if !ok {
			return false
		}
	}
	return true
}
