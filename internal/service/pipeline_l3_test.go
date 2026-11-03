package service_test

import (
	"context"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/keymanager"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/runespacecrypto"
	"github.com/CryptoLabInc/rune-mcp/internal/domain"
	"github.com/CryptoLabInc/rune-mcp/internal/service"
)

// TestPipelineL3 exercises CaptureService.Handle and RecallService.Handle against
// a LIVE disposable or freshly seeded runed + Console + RuneSpace stack. It
// stores a real Record with a unique insight; it is not MCP-process E2E.
//
// Explicit opt-in is required because this test writes persistent records:
// RUNECONSOLE_ADDR=127.0.0.1:50051 RUNE_EMBEDDER_SOCKET=/path/runed.sock RUNE_L3_DISPOSABLE_STACK=1 go test ./internal/service -run PipelineL3 -v
func TestPipelineL3(t *testing.T) {
	addr := os.Getenv("RUNECONSOLE_ADDR")
	if addr == "" {
		t.Skip("set RUNECONSOLE_ADDR to run the live L3 pipeline test")
	}
	socket := os.Getenv("RUNE_EMBEDDER_SOCKET")
	if socket == "" {
		t.Fatal("set RUNE_EMBEDDER_SOCKET to the disposable stack's runed socket")
	}
	if os.Getenv("RUNE_L3_DISPOSABLE_STACK") != "1" {
		t.Fatal("use a disposable or freshly seeded stack and set RUNE_L3_DISPOSABLE_STACK=1; this test stores a record")
	}
	token := os.Getenv("RUNECONSOLE_TOKEN")
	if token == "" {
		token = "evt_0000000000000000000000000000test"
	}
	// Isolate key storage so we do not touch a real ~/.rune.
	t.Setenv("RUNE_HOME", t.TempDir())

	// TLS only: verify + pin against RUNECONSOLE_CA (the 3-stage bootstrap's
	// persisted CA) when set, else the system CA bundle.
	opts := console.ClientOpts{}
	if ca := os.Getenv("RUNECONSOLE_CA"); ca != "" {
		opts = console.ClientOpts{CACertPath: ca}
	}
	vc, err := console.NewClient(addr, token, opts)
	if err != nil {
		t.Fatalf("console.NewClient: %v", err)
	}
	defer vc.Close()

	emb, err := embedder.New(socket, embedder.Opts{})
	if err != nil {
		t.Fatalf("embedder.New: %v", err)
	}
	defer emb.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	// ── manifest → EncKey → encryptor ──
	bundle, err := vc.GetAgentManifest(ctx)
	if err != nil {
		t.Fatalf("GetAgentManifest: %v", err)
	}
	if !slices.Contains(bundle.Capabilities, "metadata_envelope_v2") {
		t.Fatal("Console must advertise metadata_envelope_v2; upgrade Console before capture")
	}
	keyDir, err := keymanager.SaveEncKeys(bundle.KeyID, bundle.RMPEncKey, bundle.MMEncKey)
	if err != nil {
		t.Fatalf("SaveEncKeys: %v", err)
	}
	enc, err := runespacecrypto.Open(keyDir, bundle.KeyID, bundle.Dim)
	if err != nil {
		t.Fatalf("open encryptor: %v", err)
	}
	defer enc.Close()

	// ── centroid relay → runed ──
	cs, err := vc.Centroids(ctx)
	if err != nil {
		t.Fatalf("Centroids relay: %v", err)
	}
	if err := emb.SetCentroids(ctx, cs.Version, cs.Dim, cs.Preset, cs.Vectors); err != nil {
		t.Fatalf("SetCentroids to runed: %v", err)
	}
	t.Logf("centroids synced: version=%s nlist=%d", cs.Version, len(cs.Vectors))

	// Capture through the production service, including Record construction.
	insight := "The team chose PostgreSQL for the ledger because ACID transactions were non-negotiable. Run " + uuid.NewString()
	detail := "Disposable L3 integration record"
	capture := service.NewCaptureService()
	capture.SetDependencies(service.CaptureDependencies{
		Console: vc, Embedder: emb, Encryptor: enc, AgentID: bundle.AgentID, AgentDEK: bundle.AgentDEK,
		Capabilities: bundle.Capabilities, Author: "pipeline-l3",
	})
	result, err := capture.Handle(ctx, &domain.CaptureRequest{Insight: insight, Context: detail})
	if err != nil {
		t.Fatalf("CaptureService.Handle: %v", err)
	}
	if !result.OK || !result.Captured || result.RecordID == "" {
		t.Fatalf("capture response: %+v", result)
	}
	recall := service.NewRecallService()
	recall.Console = vc
	recall.Embedder = emb
	recalled, err := recall.Handle(ctx, &domain.RecallArgs{Query: insight, TopK: 5})
	if err != nil {
		t.Fatalf("RecallService.Handle: %v", err)
	}
	for _, entry := range recalled.Results {
		if entry.RecordID == result.RecordID {
			if entry.Insight != insight || entry.Context != detail || entry.Author != "pipeline-l3" {
				t.Fatalf("record changed in round trip: %+v", entry)
			}
			return
		}
	}
	t.Fatal("captured Record not found in recall with exact opened content")
}
