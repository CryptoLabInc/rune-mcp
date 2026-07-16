package lifecycle

import (
	"bytes"
	"strings"
	"testing"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
)

func goodBundle() *console.Bundle {
	return &console.Bundle{
		AgentID:   "agent-1",
		KeyID:     "runeconsole-key",
		Dim:       1024,
		RMPEncKey: []byte(`{"k":"rmp"}`),
		MMEncKey:  []byte{1, 2, 3},
		AgentDEK:  bytes.Repeat([]byte{7}, 32),
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
		mutate  func(*console.Bundle)
		wantSub string
	}{
		{"missing RMP EncKey", func(b *console.Bundle) { b.RMPEncKey = nil }, "rmp_enc_key is empty"},
		{"missing MM key", func(b *console.Bundle) { b.MMEncKey = nil }, "mm_enc_key is empty"},
		{"missing dek", func(b *console.Bundle) { b.AgentDEK = nil }, "agent_dek is 0 bytes"},
		{"short dek", func(b *console.Bundle) { b.AgentDEK = []byte{1, 2, 3} }, "agent_dek is 3 bytes"},
		{"missing agent id", func(b *console.Bundle) { b.AgentID = "" }, "agent_id is empty"},
		{"missing key id", func(b *console.Bundle) { b.KeyID = "" }, "key_id is empty"},
		{"zero dim", func(b *console.Bundle) { b.Dim = 0 }, "dim is 0"},
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
