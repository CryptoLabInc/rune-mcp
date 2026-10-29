// Package seal produces authenticated client-side metadata envelopes. The
// Console receives ciphertext, validates and opens it on insert, and opens it
// again on recall using the same derived agent key and authenticated identity.
package seal

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
)

type envelope struct {
	V int    `json:"v"`
	A string `json:"a"`
	R string `json:"r"`
	C string `json:"c"`
}

// SealV2 uses an HKDF-separated AES-256-GCM key and a fresh 12-byte nonce.
// Agent and outer storage record IDs are authenticated with the ciphertext.
func SealV2(baseDEK []byte, agentID, recordID string, plaintext []byte) (string, error) {
	if len(baseDEK) != 32 {
		return "", fmt.Errorf("seal: invalid DEK size %d (expected 32)", len(baseDEK))
	}
	if agentID == "" || recordID == "" {
		return "", fmt.Errorf("seal: agent ID and record ID must be nonempty")
	}
	key, err := hkdf.Key(sha256.New, baseDEK, nil, "rune/metadata/aes256gcm/v2", 32)
	if err != nil {
		return "", fmt.Errorf("seal: derive key: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", fmt.Errorf("seal: aes: %w", err)
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", fmt.Errorf("seal: gcm: %w", err)
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err = io.ReadFull(rand.Reader, nonce); err != nil {
		return "", fmt.Errorf("seal: random nonce: %w", err)
	}
	aad, err := json.Marshal([]string{"rune-metadata", "2", agentID, recordID})
	if err != nil {
		return "", fmt.Errorf("seal: marshal AAD: %w", err)
	}
	payload := gcm.Seal(nonce, nonce, plaintext, aad)
	data, err := json.Marshal(envelope{V: 2, A: agentID, R: recordID, C: base64.StdEncoding.EncodeToString(payload)})
	if err != nil {
		return "", fmt.Errorf("seal: marshal envelope: %w", err)
	}
	return string(data), nil
}
