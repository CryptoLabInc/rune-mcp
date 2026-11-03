// Package keymanager persists FHE key material received from Console to the
// local rune directory so the runespace SDK can load it via OpenKeys.
//
// Format note: EncKey.json carries a libevi key envelope (provider_meta +
// entries — see third_party/evi/include/km/KeyEnvelope.hpp). runespace-sdk
// (our runespace adapter) is a libevi wrapper that produces / consumes this
// same on-disk format. The Console server generated the key via
// runespace-sdk's GenerateKeys (which calls libevi's evi_km_wrap_enc_key)
// and forwards the file content verbatim through GetAgentManifest's
// manifest_json. When we load it back, runespace-sdk's OpenKeys
// invokes evi_km_unwrap_enc_key — which expects the same envelope shape on
// disk. We must persist bundle.RMPEncKey / bundle.MMEncKey byte-identical; any
// re-encoding or re-wrapping will be rejected by the cgo unwrap.
package keymanager

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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
//
// The directory is content-addressed, <keyID>@<digest> (see encKeyDigest), so
// it only ever holds this exact pair. Every Console sends the same key ID,
// parallel sessions share ~/.rune/keys, and an Encryptor re-reads its
// directory whenever it reloads after idle. With one directory per key ID, a
// boot against another Console replaced the EncKey under a running session,
// which then encrypted captures for its own Console with the other Console's
// key. Superseded directories are left in place because another live session
// may still reload from one.
func SaveEncKeys(keyID string, rmpJSON, mmJSON []byte) (string, error) {
	if len(rmpJSON) == 0 || len(mmJSON) == 0 {
		return "", fmt.Errorf("keymanager: empty EncKey material (rmp=%d mm=%d)", len(rmpJSON), len(mmJSON))
	}
	if err := CheckKeyID(keyID); err != nil {
		return "", fmt.Errorf("keymanager: %w", err)
	}
	keyDir, err := KeyDir(keyID + "@" + encKeyDigest(rmpJSON, mmJSON))
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
	if err := writeFileAtomic(filepath.Join(rmpDir, "EncKey.json"), rmpJSON, config.FilePerm); err != nil {
		return "", fmt.Errorf("keymanager: write rmp/EncKey.json: %w", err)
	}
	if err := writeFileAtomic(filepath.Join(mmDir, "EncKey.json"), mmJSON, config.FilePerm); err != nil {
		return "", fmt.Errorf("keymanager: write mm/EncKey.json: %w", err)
	}
	return keyDir, nil
}

// writeFileAtomic writes data to path via a same-directory temp file plus
// rename, so a concurrent reader sees either the old or the new envelope,
// never a truncated one. Readers are no longer boot-only: the lazy encryptor
// re-reads EncKey.json on every on-demand load, and parallel agent sessions
// each run their own boot — plain WriteFile raced them (observed as
// evi_km_unwrap_enc_key "unterminated 'key_data'" failures under 20 parallel
// boots).
func writeFileAtomic(path string, data []byte, perm os.FileMode) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), ".enckey-tmp-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName) // no-op after a successful rename
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmpName, path)
}

// MaxKeyIDLen keeps <keyID>@<24-hex digest> within the 255-byte file-name
// limit of the supported file systems.
const MaxKeyIDLen = 255 - 1 - 24

// CheckKeyID reports why a Console-supplied key ID cannot name a key
// directory, or nil. The ID becomes one path element under ~/.rune/keys.
func CheckKeyID(keyID string) error {
	switch {
	case keyID == "":
		return fmt.Errorf("key_id is empty")
	case keyID == "." || keyID == ".." || strings.ContainsAny(keyID, "/\\\x00"):
		return fmt.Errorf("key_id %q is not a single path element", keyID)
	case len(keyID) > MaxKeyIDLen:
		return fmt.Errorf("key_id is %d bytes (max %d)", len(keyID), MaxKeyIDLen)
	}
	return nil
}

// encKeyDigest identifies one EncKey pair: the first 24 hex characters (96
// bits) of SHA-256 over len(rmpJSON) as 8 big-endian bytes, rmpJSON, then
// mmJSON. The length prefix keeps the boundary between the two envelopes
// unambiguous.
func encKeyDigest(rmpJSON, mmJSON []byte) string {
	h := sha256.New()
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(rmpJSON)))
	h.Write(n[:])
	h.Write(rmpJSON)
	h.Write(mmJSON)
	return hex.EncodeToString(h.Sum(nil))[:24]
}

// KeyDir returns ~/.rune/keys/<name>/, the directory the runespace SDK's
// OpenKeys expects as WithKeyPath. The SDK resolves each tier's EncKey under
// its subdirectory (rmp/EncKey.json, mm/EncKey.json) and does not use the
// directory name. SaveEncKeys names the directory <keyID>@<digest>.
func KeyDir(name string) (string, error) {
	runedir, err := config.RuneDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(runedir, "keys", name), nil
}
