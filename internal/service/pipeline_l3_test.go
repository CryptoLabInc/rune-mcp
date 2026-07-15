package service_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/embedder"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/keymanager"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/runespacecrypto"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/seal"
)

// TestPipelineL3 exercises the full client-side-crypto pipeline through the
// mcp-side clients against a LIVE local stack (runed + runeconsole + runespace):
//
//	manifest → save EncKey → open encryptor (cgo)
//	centroid relay → runed SetCentroids
//	text → EmbedRoute → EncryptFlat/Clustered + seal → console.Insert (forward)
//	query → EmbedSingle → console.Search → opened plaintext hit
//
// Gated on RUNECONSOLE_ADDR (e.g. 127.0.0.1:50051). The console must be a build
// that distributes EncKey/agent_dek (pre_encrypted capability).
//
//	RUNECONSOLE_ADDR=127.0.0.1:50051 RUNE_HOME=$(mktemp -d) go test ./internal/service -run PipelineL3 -v
func TestPipelineL3(t *testing.T) {
	addr := os.Getenv("RUNECONSOLE_ADDR")
	if addr == "" {
		t.Skip("set RUNECONSOLE_ADDR to run the live L3 pipeline test")
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

	emb, err := embedder.New(embedder.ResolveSocketPath(""), embedder.Opts{})
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

	// ── capture: embed+route → encrypt → seal → forward ──
	insight := "The team chose PostgreSQL over MongoDB for the ledger because ACID transactions were non-negotiable. " + t.Name()
	routed, err := emb.EmbedRoute(ctx, insight)
	if err != nil {
		t.Fatalf("EmbedRoute: %v", err)
	}
	rmp, err := enc.EncryptFlat(routed.Vector)
	if err != nil {
		t.Fatalf("EncryptFlat: %v", err)
	}
	mm, err := enc.EncryptClustered(routed.Vector)
	if err != nil {
		t.Fatalf("EncryptClustered: %v", err)
	}
	meta, _ := json.Marshal(map[string]any{
		"id":               "rec-db-choice",
		"title":            "Database choice",
		"reusable_insight": insight,
		"domain":           "architecture",
	})
	sealed, err := seal.Seal(bundle.AgentDEK, bundle.AgentID, meta)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	id, err := vc.Insert(ctx, console.InsertItem{
		ID:                 uuid.NewString(), // runespace requires a UUID item id
		RMPItem:            rmp,
		MMItem:             mm,
		ClusterID:          routed.ClusterID,
		CentroidSetVersion: routed.CentroidSetVersion,
		SealedMetadata:     sealed,
	})
	if err != nil {
		t.Fatalf("console.Insert: %v", err)
	}
	t.Logf("capture ok: id=%s (client-encrypted)", id)

	// ── recall: plaintext query → console decrypts + opens ──
	qvec, err := emb.EmbedSingle(ctx, "which database did we pick for the ledger and why?")
	if err != nil {
		t.Fatalf("embed query: %v", err)
	}
	hits, err := vc.Search(ctx, qvec, 5)
	if err != nil {
		t.Fatalf("console.Search: %v", err)
	}
	t.Logf("recall returned %d hits", len(hits))
	for i, h := range hits {
		t.Logf("  hit[%d] id=%s score=%.4f meta=%s", i, h.ID, h.Score, h.Metadata)
	}
	if len(hits) == 0 {
		t.Fatal("recall returned 0 hits")
	}
	found := false
	for _, h := range hits {
		if strings.Contains(h.Metadata, "PostgreSQL") && strings.Contains(h.Metadata, "Database choice") {
			found = true
		}
	}
	if !found {
		t.Fatal("captured record not found in recall with opened plaintext metadata")
	}
	_ = filepath.Separator
}
