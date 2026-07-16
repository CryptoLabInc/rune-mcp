// Package keymanager persists FHE key material received from Console to the
// local rune directory so the runespace SDK can load it via OpenKeysFromFile.
//
// Format note: EncKey.json carries a libevi key envelope (provider_meta +
// entries — see third_party/evi/include/km/KeyEnvelope.hpp). runespace-sdk
// (our runespace adapter) is a libevi wrapper that produces / consumes this
// same on-disk format. The Console server generated the key via
// runespace-sdk's GenerateKeys (which calls libevi's evi_km_wrap_enc_key)
// and forwards the file content verbatim through GetAgentManifest's
// manifest_json. When we load it back, runespace-sdk's OpenKeysFromFile
// invokes evi_km_unwrap_enc_key — which expects the same envelope shape on
// disk. We must persist bundle.RMPEncKey / bundle.MMEncKey byte-identical; any
// re-encoding or re-wrapping will be rejected by the cgo unwrap.
package keymanager

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/config"
)

// SaveEncKeys writes both PUBLIC encryption keys in the runespace-go-sdk
// on-disk layout so runespacecrypto.Open (WithKeyPath keyDir) can load them,
// one tier per subdirectory:
//
//	<keyDir>/rmp/EncKey.json   RMP EncKey envelope (verbatim, for EncryptFlat)
//	<keyDir>/mm/EncKey.json    MM EncKey envelope (verbatim, for EncryptClustered)
//
// Both are delivered as verbatim JSON envelopes in the Console manifest
// (rmpJSON from "rmp_enc_key", mmJSON from "mm_enc_key"). Written verbatim —
// any re-encoding breaks the cgo key loader. Returns the key directory. Empty
// inputs are an error (a manifest missing either key cannot support client
// encryption).
func SaveEncKeys(keyID string, rmpJSON, mmJSON []byte) (string, error) {
	if len(rmpJSON) == 0 || len(mmJSON) == 0 {
		return "", fmt.Errorf("keymanager: empty EncKey material (rmp=%d mm=%d)", len(rmpJSON), len(mmJSON))
	}
	keyDir, err := KeyDir(keyID)
	if err != nil {
		return "", err
	}
	rmpDir := filepath.Join(keyDir, "rmp")
	mmDir := filepath.Join(keyDir, "mm")
	for _, dir := range []string{rmpDir, mmDir} {
		if err := os.MkdirAll(dir, config.DirPerm); err != nil {
			return "", fmt.Errorf("keymanager: mkdir %s: %w", dir, err)
		}
	}
	// MkdirAll honors umask — enforce 0700 on the key root and both tier dirs.
	for _, dir := range []string{keyDir, rmpDir, mmDir} {
		if err := os.Chmod(dir, config.DirPerm); err != nil {
			return "", fmt.Errorf("keymanager: chmod %s: %w", dir, err)
		}
	}
	if err := os.WriteFile(filepath.Join(rmpDir, "EncKey.json"), rmpJSON, config.FilePerm); err != nil {
		return "", fmt.Errorf("keymanager: write rmp/EncKey.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(mmDir, "EncKey.json"), mmJSON, config.FilePerm); err != nil {
		return "", fmt.Errorf("keymanager: write mm/EncKey.json: %w", err)
	}
	return keyDir, nil
}

// KeyDir returns the per-key directory path that the runespace SDK's
// OpenKeysFromFile expects as WithKeyPath: ~/.rune/keys/<keyID>/. The SDK
// resolves each tier's EncKey under its subdirectory (rmp/EncKey.json,
// mm/EncKey.json).
func KeyDir(keyID string) (string, error) {
	runedir, err := config.RuneDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(runedir, "keys", keyID), nil
}
