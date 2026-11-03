package runespacecrypto

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/keymanager"
)

func readEncKeyPair(t *testing.T, dir string) (rmp, mm []byte) {
	t.Helper()
	var err error
	if rmp, err = os.ReadFile(filepath.Join(dir, "rmp", "EncKey.json")); err != nil {
		t.Fatalf("read rmp EncKey: %v", err)
	}
	if mm, err = os.ReadFile(filepath.Join(dir, "mm", "EncKey.json")); err != nil {
		t.Fatalf("read mm EncKey: %v", err)
	}
	return rmp, mm
}

// TestReloadKeepsBootKeyAfterAnotherBoot is the end-to-end regression for
// parallel sessions sharing ~/.rune/keys. A session boots against Console A,
// another boot then saves Console B's EncKey pair under the same key ID, and
// the first session's Encryptor reloads after idle. That reload must still
// read A's EncKey: with a single per-key-ID directory it read B's, so captures
// accepted by A were encrypted for B.
//
// B's pair here cannot be unwrapped, so a reload that opens any directory
// other than A's fails loudly. A real B key would encrypt successfully, and a
// successful encrypt says nothing about which key was used.
func TestReloadKeepsBootKeyAfterAnotherBoot(t *testing.T) {
	rmpA, mmA := readEncKeyPair(t, genKeys(t))

	t.Setenv("HOME", t.TempDir())
	t.Setenv("RUNE_ENCRYPTOR_IDLE", "0") // unload after every call: each encrypt reloads from disk

	dirA, err := keymanager.SaveEncKeys("t", rmpA, mmA)
	if err != nil {
		t.Fatalf("SaveEncKeys(A): %v", err)
	}
	e, err := Open(dirA, "t", 128)
	if err != nil {
		t.Fatalf("Open(A): %v", err)
	}
	defer e.Close()

	vec := make([]float32, 128)
	vec[0] = 1
	if _, err := e.EncryptFlat(vec); err != nil {
		t.Fatalf("EncryptFlat before the other boot: %v", err)
	}
	if e.loaded() {
		t.Fatal("key set still resident with RUNE_ENCRYPTOR_IDLE=0; the reload below would not read disk")
	}

	dirB, err := keymanager.SaveEncKeys("t", []byte(`{"console":"B"}`), []byte(`{"console":"B"}`))
	if err != nil {
		t.Fatalf("SaveEncKeys(B): %v", err)
	}
	if dirB == dirA {
		t.Fatalf("Console B's boot wrote into session A's key directory %q", dirA)
	}

	if _, err := e.EncryptFlat(vec); err != nil {
		t.Fatalf("EncryptFlat after the other boot (reload must read %s): %v", filepath.Base(dirA), err)
	}
	if _, err := e.EncryptClustered(vec); err != nil {
		t.Fatalf("EncryptClustered after the other boot (reload must read %s): %v", filepath.Base(dirA), err)
	}
	gotRMP, gotMM := readEncKeyPair(t, dirA)
	if !bytes.Equal(gotRMP, rmpA) || !bytes.Equal(gotMM, mmA) {
		t.Fatal("session A's key directory no longer holds the EncKey its boot saved")
	}
}
