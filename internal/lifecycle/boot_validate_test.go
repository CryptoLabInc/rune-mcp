package lifecycle

import (
	"bytes"
	"strings"
	"testing"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/vault"
)

func goodBundle() *vault.Bundle {
	return &vault.Bundle{
		AgentID:    "agent-1",
		KeyID:      "vault-key",
		IndexName:  "rune",
		Dim:        1024,
		EncKeyJSON: []byte(`{"k":"rmp"}`),
		MMEncKey:   []byte{1, 2, 3},
		AgentDEK:   bytes.Repeat([]byte{7}, 32),
	}
}

// B1 gate: every format defect must be named at receipt, and a good bundle
// must pass untouched.
func TestValidateBundle(t *testing.T) {
	if msg := validateBundle(goodBundle()); msg != "" {
		t.Fatalf("good bundle rejected: %s", msg)
	}
	cases := []struct {
		name    string
		mutate  func(*vault.Bundle)
		wantSub string
	}{
		{"missing EncKey", func(b *vault.Bundle) { b.EncKeyJSON = nil }, "EncKey.json is empty"},
		{"missing MM key", func(b *vault.Bundle) { b.MMEncKey = nil }, "mm_enc_key is empty"},
		{"missing dek", func(b *vault.Bundle) { b.AgentDEK = nil }, "agent_dek is 0 bytes"},
		{"short dek", func(b *vault.Bundle) { b.AgentDEK = []byte{1, 2, 3} }, "agent_dek is 3 bytes"},
		{"missing agent id", func(b *vault.Bundle) { b.AgentID = "" }, "agent_id is empty"},
		{"missing key id", func(b *vault.Bundle) { b.KeyID = "" }, "key_id is empty"},
		{"zero dim", func(b *vault.Bundle) { b.Dim = 0 }, "dim is 0"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := goodBundle()
			tc.mutate(b)
			msg := validateBundle(b)
			if msg == "" {
				t.Fatal("defect not detected")
			}
			if !strings.Contains(msg, tc.wantSub) {
				t.Fatalf("msg %q lacks %q", msg, tc.wantSub)
			}
		})
	}
}
