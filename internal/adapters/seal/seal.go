// Package seal produces the client-side AES-256-CTR metadata envelope used
// on the capture path. rune-mcp holds the agent_dek (delivered in the Console
// manifest) and seals metadata locally, so plaintext metadata never leaves
// the developer machine; the Console opens the envelope on recall (it owns
// team_secret and every agent's dek).
//
// Envelope: {"a": agent_id, "c": base64(IV(16B) || AES-256-CTR(dek, plaintext))}.
//
// AES-CTR is unauthenticated (no MAC/AAD). A wrong-key open yields garbage
// rather than an error; the Console guards that on recall (utf8 check) and the
// planned AES-GCM migration removes the malleability. Restored from the
// pre-integration adapter (git fe751b4^ internal/adapters/envector/aes_ctr.go);
// Open lives on the Console, not here.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

// envelope is the on-wire/at-rest shape; field names are the stable contract
// the Console's openMeta unmarshals.
type envelope struct {
	A string `json:"a"` // agent_id
	C string `json:"c"` // base64(IV || ciphertext)
}

// Seal encrypts plaintext with AES-256-CTR under dek (32 bytes) and a fresh
// random IV, returning the JSON envelope. agentID is stored so the Console can
// re-derive the same dek on open.
func Seal(dek []byte, agentID string, plaintext []byte) (string, error) {
	if len(dek) != 32 {
		return "", fmt.Errorf("seal: invalid DEK size %d (expected 32)", len(dek))
	}
	block, err := aes.NewCipher(dek)
	if err != nil {
		return "", fmt.Errorf("seal: aes.NewCipher: %w", err)
	}
	iv := make([]byte, aes.BlockSize)
	if _, err := io.ReadFull(rand.Reader, iv); err != nil {
		return "", fmt.Errorf("seal: rand IV: %w", err)
	}
	ct := make([]byte, len(plaintext))
	cipher.NewCTR(block, iv).XORKeyStream(ct, plaintext)

	combined := make([]byte, 0, len(iv)+len(ct))
	combined = append(combined, iv...)
	combined = append(combined, ct...)
	env := envelope{A: agentID, C: base64.StdEncoding.EncodeToString(combined)}
	data, err := json.Marshal(env)
	if err != nil {
		return "", fmt.Errorf("seal: marshal envelope: %w", err)
	}
	return string(data), nil
}
