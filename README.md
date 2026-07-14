# rune-mcp

A session-local MCP server for Rune's encrypted organizational memory. It is a Go
port of the agent-delegated path of Python rune v0.3.x.

An agent host (Claude Code, Codex, etc.) spawns one instance per session over stdio.
It takes capture/recall requests, runs embedding and client-side encryption, and
sends them to Console over gRPC. Console holds all key management and decryption and is
the sole client of the runespace vector engine (storage / FHE search).

## Build / Run

```sh
go build ./...
go build -o rune-mcp ./cmd/rune-mcp   # build the binary
./rune-mcp --version
```

As an MCP server it is normally started over stdio by the agent host rather than run directly.

## Layout

```
cmd/rune-mcp        entrypoint (stdio + boot loop)
internal/mcp        MCP SDK wiring · 10 tool handlers · state gate
internal/service    capture / recall / lifecycle orchestration
internal/policy     pure logic (novelty · rerank · query · PII redaction)
internal/adapters   external I/O (console gRPC · runespace crypto SDK · embedder · config)
internal/domain     core types (leaf — no imports from other internal packages)
internal/lifecycle  state machine · boot retry · graceful shutdown
internal/obs        slog + request_id + sensitive-data redaction
```

Dependency direction: `mcp → service → {policy, adapters, lifecycle, obs} → domain`.
Reverse imports are forbidden.

State machine: `starting → waiting_for_console → active ↔ dormant`. Before boot
completes, write tools are rejected with `PIPELINE_NOT_READY` and only read-only
tools run in a degraded mode.

## MCP tools (10)

`activate` · `capture` · `batch_capture` · `recall` · `capture_history` ·
`delete_capture` · `configure` · `diagnostics` · `console_status` · `reload_pipelines`

## Docs

## Dependencies

- `runed` — shared daemon runtime
- runespace Go SDK — client-side FHE encryption (developed separately)
- Console gRPC — key management · FHE decryption
- MCP Go SDK — `modelcontextprotocol/go-sdk`
