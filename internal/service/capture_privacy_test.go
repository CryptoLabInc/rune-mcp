package service

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
	"github.com/CryptoLabInc/rune-mcp/internal/domain"
)

// These boundary spies catch any return of the plaintext novelty-search path.
// Routing, sealing, response construction, and retry orchestration stay real.
type privacyEmbedder struct {
	*resyncEmbedder
	singleCalls int
}

func (e *privacyEmbedder) EmbedSingle(context.Context, string) ([]float32, error) {
	e.singleCalls++
	return []float32{1, 0}, nil
}

type privacyConsole struct {
	*resyncConsole
	searchCalls int
	items       []console.InsertItem
}

func (c *privacyConsole) Search(context.Context, []float32, int) ([]console.Hit, error) {
	c.searchCalls++
	return []console.Hit{{ID: "already-stored", Score: 1}}, nil
}
func (c *privacyConsole) Insert(ctx context.Context, item console.InsertItem) (string, error) {
	c.items = append(c.items, item)
	return c.resyncConsole.Insert(ctx, item)
}

func TestCaptureHandlePrivacy(t *testing.T) {
	for _, tc := range []struct {
		name                                string
		routeVersion                        string
		wantInserts, wantRoutes, wantPushes int
	}{
		{"ordinary", "v2", 1, 1, 0},
		{"centroid version retry", "v1", 2, 2, 1},
		{"cold centroid resync", "", 1, 2, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := &privacyEmbedder{resyncEmbedder: &resyncEmbedder{version: tc.routeVersion}}
			c := &privacyConsole{resyncConsole: &resyncConsole{engineVersion: "v2"}}
			s := newResyncService(e, c.resyncConsole)
			d := s.Dependencies()
			d.Console = c
			s.SetDependencies(d)
			s.Now = func() time.Time { return time.Date(2026, 10, 28, 0, 0, 0, 0, time.UTC) }
			req := &domain.CaptureRequest{Insight: "Private decision and rationale", Context: "Private supporting details"}
			resp, err := s.Handle(context.Background(), req)
			if err != nil {
				t.Fatalf("Handle: %v", err)
			}
			if c.searchCalls != 0 || e.singleCalls != 0 {
				t.Errorf("plaintext novelty path invoked: Search=%d EmbedSingle=%d; want both zero", c.searchCalls, e.singleCalls)
			}
			if !resp.OK || !resp.Captured || resp.RecordID == "" {
				t.Fatalf("capture did not succeed: %+v", resp)
			}
			raw, err := json.Marshal(resp)
			if err != nil {
				t.Fatal(err)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(raw, &fields); err != nil {
				t.Fatal(err)
			}
			if _, exists := fields["novelty"]; exists {
				t.Error("novelty must be omitted from response JSON")
			}
			if len(c.items) != tc.wantInserts || e.routeCalls != tc.wantRoutes || e.pushCalls != tc.wantPushes {
				t.Fatalf("insert=%d route=%d push=%d; want %d/%d/%d", len(c.items), e.routeCalls, e.pushCalls, tc.wantInserts, tc.wantRoutes, tc.wantPushes)
			}
			for _, item := range c.items {
				if item.ID != resp.RecordID {
					t.Errorf("retry changed logical ID: %q != %q", item.ID, resp.RecordID)
				}
				if !bytes.Equal(item.RMPItem, []byte{0xAA}) || !bytes.Equal(item.MMItem, []byte{0xBB}) {
					t.Error("insert did not contain encrypted vectors")
				}
				if bytes.Contains([]byte(item.SealedMetadata), []byte(req.Insight)) || bytes.Contains([]byte(item.SealedMetadata), []byte(req.Context)) {
					t.Error("insert contains plaintext metadata")
				}
				var env struct {
					V int    `json:"v"`
					R string `json:"r"`
					A string `json:"a"`
					C string `json:"g"`
				}
				if err := json.Unmarshal([]byte(item.SealedMetadata), &env); err != nil {
					t.Fatal(err)
				}
				if env.V != 2 || env.R != item.ID {
					t.Fatalf("metadata must authenticate outer ID in v2 envelope: %+v", env)
				}
				if env.A != "agent-test" {
					t.Errorf("seal agent = %q", env.A)
				}
				encrypted, err := base64.StdEncoding.DecodeString(env.C)
				if err != nil {
					t.Fatal(err)
				}
				if len(encrypted) < 28 {
					t.Fatal("sealed metadata missing ciphertext")
				}
				key, err := hkdf.Key(sha256.New, s.Dependencies().AgentDEK, nil, "rune/metadata/aes256gcm/v2", 32)
				if err != nil {
					t.Fatal(err)
				}
				block, err := aes.NewCipher(key)
				if err != nil {
					t.Fatal(err)
				}
				gcm, err := cipher.NewGCM(block)
				if err != nil {
					t.Fatal(err)
				}
				aad, _ := json.Marshal([]string{"rune-metadata", "2", env.A, item.ID})
				plain, err := gcm.Open(nil, encrypted[:12], encrypted[12:], aad)
				if err != nil {
					t.Fatalf("authenticate inserted metadata: %v", err)
				}
				var record domain.Record
				if err := json.Unmarshal(plain, &record); err != nil {
					t.Fatalf("open inserted metadata: %v", err)
				}
				if record.Insight != req.Insight || record.Context != req.Context {
					t.Errorf("inserted record lost content: %+v", record)
				}
			}
			if last := c.items[len(c.items)-1]; last.CentroidSetVersion != "v2" || last.ClusterID != 3 {
				t.Errorf("insert routing = %+v", last)
			}
		})
	}
}

func TestCaptureMissingMetadataCapability(t *testing.T) {
	e := &privacyEmbedder{resyncEmbedder: &resyncEmbedder{version: "v2"}}
	c := &privacyConsole{resyncConsole: &resyncConsole{engineVersion: "v2"}}
	s := newResyncService(e, c.resyncConsole)
	d := s.Dependencies()
	d.Console = c
	s.SetDependencies(d)
	// Default construction represents a server that did not negotiate v2.
	d.Capabilities = nil
	s.SetDependencies(d)
	_, err := s.EncryptSealInsert(context.Background(), "private", `{}`)
	var re *domain.RuneError
	if !errors.As(err, &re) || re.Code != domain.CodeConsoleUpgradeRequired || re.Retryable || re.RecoveryHint == "" {
		t.Fatalf("want actionable capability error, got %v", err)
	}
	if e.routeCalls != 0 || e.singleCalls != 0 || len(c.items) != 0 || c.searchCalls != 0 {
		t.Fatalf("capture used adapters before capability refusal")
	}
}

func TestCaptureRejectsOversizedAuthorBeforeEmbedding(t *testing.T) {
	e := &privacyEmbedder{resyncEmbedder: &resyncEmbedder{version: "v2"}}
	c := &privacyConsole{resyncConsole: &resyncConsole{engineVersion: "v2"}}
	s := newResyncService(e, c.resyncConsole)
	d := s.Dependencies()
	d.Console = c
	d.Author = string(bytes.Repeat([]byte{'x'}, domain.MaxAuthorBytes+1))
	s.SetDependencies(d)
	_, err := s.Handle(t.Context(), &domain.CaptureRequest{Insight: "valid"})
	var re *domain.RuneError
	if !errors.As(err, &re) || re.Code != domain.CodeInvalidInput {
		t.Fatalf("oversized author error: %v", err)
	}
	if e.routeCalls != 0 || e.singleCalls != 0 || len(c.items) != 0 || c.searchCalls != 0 {
		t.Fatal("invalid content used adapters")
	}
}
