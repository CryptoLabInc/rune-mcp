// Package keymanager persists FHE key material received from Console to the
// local rune directory so the runespace SDK can load it via OpenKeysFromFile.
//
// Format note: EncKey.json carries a libevi key envelope (provider_meta +
// entries — see third_party/evi/include/km/KeyEnvelope.hpp). runespace-sdk
// (our runespace adapter) and pyenvector are both libevi wrappers and produce
// / consume this same on-disk format. The Console server generated the key via
// runespace-sdk's GenerateKeys (which calls libevi's evi_km_wrap_enc_key)
// and forwards the file content verbatim through GetAgentManifest's
// manifest_json. When we load it back, runespace-sdk's OpenKeysFromFile
// invokes evi_km_unwrap_enc_key — which expects the same envelope shape on
// disk. We must persist bundle.EncKey byte-identical; any re-encoding or
// re-wrapping will be rejected by the cgo unwrap.
package keymanager

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/config"
)

// SaveEncKey writes the EncKey envelope received from Console verbatim to
// ~/.rune/keys/<keyID>/EncKey.json (perm 0600). The directory is created
// with perm 0700 if missing.
//
// encKey is the byte content of the original libevi EncKey.json file
// (manifest_json field "EncKey.json" carries this as a string). Do NOT
// re-encode, base64-wrap, or otherwise transform — the cgo unwrap on the
// runespace side parses the original envelope shape and any modification
// breaks it.
//
// Empty encKey is treated as a no-op (caller responsibility to validate).
func SaveEncKey(keyID string, encKey []byte) error {
	if len(encKey) == 0 {
		return nil
	}

	keyDir, err := KeyDir(keyID)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(keyDir, config.DirPerm); err != nil {
		return fmt.Errorf("keymanager: mkdir %s: %w", keyDir, err)
	}
	// MkdirAll honors umask — explicitly enforce 0700 in case umask masked it.
	if err := os.Chmod(keyDir, config.DirPerm); err != nil {
		return fmt.Errorf("keymanager: chmod %s: %w", keyDir, err)
	}

	encPath := filepath.Join(keyDir, "EncKey.json")
	if err := os.WriteFile(encPath, encKey, config.FilePerm); err != nil {
		return fmt.Errorf("keymanager: write EncKey.json: %w", err)
	}
	return nil
}

// SaveEncKeys writes both PUBLIC encryption keys in the runespace-go-sdk
// on-disk layout so runespacecrypto.Open (WithKeyPath keyDir) can load them:
//
//	<keyDir>/EncKey.json      RMP EncKey envelope (verbatim, for EncryptFlat)
//	<keyDir>/mm/EncKey.bin     MM EncKey raw bytes (for EncryptClustered)
//
// Both are delivered in the Console manifest (rmpJSON as string "EncKey.json",
// mmKey base64-decoded from "mm_enc_key"). Written verbatim — any re-encoding
// breaks the cgo key loader. Returns the key directory. Empty inputs are an
// error (a manifest missing either key cannot support client encryption).
func SaveEncKeys(keyID string, rmpJSON, mmKey []byte) (string, error) {
	if len(rmpJSON) == 0 || len(mmKey) == 0 {
		return "", fmt.Errorf("keymanager: empty EncKey material (rmp=%d mm=%d)", len(rmpJSON), len(mmKey))
	}
	keyDir, err := KeyDir(keyID)
	if err != nil {
		return "", err
	}
	mmDir := filepath.Join(keyDir, "mm")
	if err := os.MkdirAll(mmDir, config.DirPerm); err != nil {
		return "", fmt.Errorf("keymanager: mkdir %s: %w", mmDir, err)
	}
	if err := os.Chmod(keyDir, config.DirPerm); err != nil {
		return "", fmt.Errorf("keymanager: chmod %s: %w", keyDir, err)
	}
	if err := os.WriteFile(filepath.Join(keyDir, "EncKey.json"), rmpJSON, config.FilePerm); err != nil {
		return "", fmt.Errorf("keymanager: write EncKey.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(mmDir, "EncKey.bin"), mmKey, config.FilePerm); err != nil {
		return "", fmt.Errorf("keymanager: write mm/EncKey.bin: %w", err)
	}
	return keyDir, nil
}

// KeyDir returns the per-key directory path that the runespace SDK's
// OpenKeysFromFile expects as WithKeyPath: ~/.rune/keys/<keyID>/. This is
// the directory containing EncKey.json — the SDK resolves the file
// directly via filepath.Join(keyDir, "EncKey.json").
func KeyDir(keyID string) (string, error) {
	runedir, err := config.RuneDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(runedir, "keys", keyID), nil
}
