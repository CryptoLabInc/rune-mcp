// Package runespacecrypto wraps the runespace-go-sdk key set for the one
// operation rune-mcp performs locally: FHE-encrypting an embedding with the
// PUBLIC EncKey before handing the ciphertext to the Console. This is the only
// cgo (libevi) surface in rune-mcp — keeping it in one package bounds the
// build/platform constraint (evi archives are arm64-only).
//
// The Console delivers the EncKey pair (RMP JSON envelope + MM raw key) in the
// agent manifest; keymanager persists them in the SDK's on-disk layout and
// Open loads them Enc-only (no SecKey — decryption stays in the Console).
package runespacecrypto

import (
	"fmt"

	runespace "github.com/CryptoLabInc/runespace-sdk"
)

// Encryptor holds an opened, Enc-only key set. It is safe for concurrent use
// only insofar as the SDK serializes cgo calls internally; treat one
// Encryptor as the process-wide encrypt handle. Close releases the cgo
// handles.
type Encryptor struct {
	keys *runespace.Keys
	dim  int
}

// Open loads the EncKey pair from keyDir (the SDK layout keymanager wrote)
// for the given key id and dimension. It requests only the Enc part, so a
// missing SecKey is fine — this side never decrypts.
func Open(keyDir, keyID string, dim int) (*Encryptor, error) {
	keys, err := runespace.OpenKeys(
		runespace.WithKeyPath(keyDir),
		runespace.WithKeyID(keyID),
		runespace.WithKeyDim(dim),
		runespace.WithKeyParts(runespace.KeyPartEnc),
	)
	if err != nil {
		return nil, fmt.Errorf("runespacecrypto: open keys: %w", err)
	}
	return &Encryptor{keys: keys, dim: dim}, nil
}

// Dim reports the FHE slot dimension the key set was opened with.
func (e *Encryptor) Dim() int { return e.dim }

// EncryptFlat produces the flat (RMP) tier ITEM ciphertext. vec must be
// l2-normalized and length Dim().
func (e *Encryptor) EncryptFlat(vec []float32) ([]byte, error) {
	if e == nil || e.keys == nil {
		return nil, fmt.Errorf("runespacecrypto: encryptor is closed")
	}
	b, err := e.keys.EncryptFlat(vec)
	if err != nil {
		return nil, fmt.Errorf("runespacecrypto: encrypt flat: %w", err)
	}
	return b, nil
}

// EncryptClustered produces the compact cluster (MM) tier ITEM ciphertext.
// Same input contract as EncryptFlat.
func (e *Encryptor) EncryptClustered(vec []float32) ([]byte, error) {
	if e == nil || e.keys == nil {
		return nil, fmt.Errorf("runespacecrypto: encryptor is closed")
	}
	b, err := e.keys.EncryptClustered(vec)
	if err != nil {
		return nil, fmt.Errorf("runespacecrypto: encrypt clustered: %w", err)
	}
	return b, nil
}

// Close releases the cgo key handles. Idempotent.
func (e *Encryptor) Close() error {
	if e == nil || e.keys == nil {
		return nil
	}
	err := e.keys.Close()
	e.keys = nil
	return err
}
