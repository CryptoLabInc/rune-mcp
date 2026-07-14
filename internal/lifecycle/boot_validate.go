package lifecycle

import (
	"fmt"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/console"
)

// agentDEKSize is the seal contract (adapters/seal: AES-256, exactly 32
// bytes). Checked here at manifest receipt so a bad console surfaces as a boot
// error instead of a far-away seal failure on the first capture (§9.1 B1).
const agentDEKSize = 32

// validateBundle reports the first format-level defect in a manifest bundle
// ("" = ok). It runs after the capability check and before SaveEncKeys, so
// bad material is rejected before it can overwrite the on-disk key copies.
// Format checks only — cryptographic validity is judged where each material
// is used (runespacecrypto.Open parses the keys; the dek proves itself at
// seal/open time).
func validateBundle(b *console.Bundle) string {
	switch {
	case len(b.EncKeyJSON) == 0:
		return "manifest defect: EncKey.json is empty"
	case len(b.MMEncKey) == 0:
		return "manifest defect: mm_enc_key is empty"
	case len(b.AgentDEK) != agentDEKSize:
		return fmt.Sprintf("manifest defect: agent_dek is %d bytes (expected %d)", len(b.AgentDEK), agentDEKSize)
	case b.AgentID == "":
		return "manifest defect: agent_id is empty"
	case b.KeyID == "":
		return "manifest defect: key_id is empty"
	case b.Dim <= 0:
		return fmt.Sprintf("manifest defect: dim is %d (expected > 0)", b.Dim)
	}
	return ""
}
