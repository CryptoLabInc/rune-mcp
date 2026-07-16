package keymanager_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/CryptoLabInc/rune-mcp/internal/adapters/config"
	"github.com/CryptoLabInc/rune-mcp/internal/adapters/keymanager"
)

// withTempHome redirects $HOME to a t.TempDir for the duration of t.
// config.RuneDir() reads $HOME, so this isolates filesystem side effects.
func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

// Representative libevi KeyEnvelope JSON payloads (provider_meta + entries),
// the exact bytes Console forwards as manifest_json's rmp_enc_key / mm_enc_key.
var (
	rmpEnvelope = []byte(`{"provider_meta":{"name":"test","format_version":"1"},"entries":[{"role":"EncKey","key_data":"AAAA","metadata":{"eval_mode":"rmp"}}]}`)
	mmEnvelope  = []byte(`{"provider_meta":{"name":"test","format_version":"1"},"entries":[{"role":"EncKey","key_data":"BBBB","metadata":{"eval_mode":"mm"}}]}`)
)

func TestSaveEncKeys_WritesBothTiersVerbatim(t *testing.T) {
	home := withTempHome(t)

	keyDir, err := keymanager.SaveEncKeys("key-test", rmpEnvelope, mmEnvelope)
	if err != nil {
		t.Fatalf("SaveEncKeys: %v", err)
	}
	if want := filepath.Join(home, ".rune", "keys", "key-test"); keyDir != want {
		t.Errorf("keyDir: got %q, want %q", keyDir, want)
	}

	// Each tier lands under its subdirectory as EncKey.json, byte-identical —
	// keymanager must NOT transform the envelope (the cgo unwrap parses it).
	for _, tc := range []struct {
		tier string
		want []byte
	}{
		{"rmp", rmpEnvelope},
		{"mm", mmEnvelope},
	} {
		got, err := os.ReadFile(filepath.Join(keyDir, tc.tier, "EncKey.json"))
		if err != nil {
			t.Fatalf("read %s/EncKey.json: %v", tc.tier, err)
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("%s: file differs from input.\nwant=%q\n got=%q", tc.tier, tc.want, got)
		}
	}
}

func TestSaveEncKeys_EmptyIsError(t *testing.T) {
	withTempHome(t)

	if _, err := keymanager.SaveEncKeys("empty-rmp", nil, mmEnvelope); err == nil {
		t.Error("empty rmp: got nil error, want error")
	}
	if _, err := keymanager.SaveEncKeys("empty-mm", rmpEnvelope, nil); err == nil {
		t.Error("empty mm: got nil error, want error")
	}
}

func TestSaveEncKeys_FileAndDirPerms(t *testing.T) {
	withTempHome(t)

	keyDir, err := keymanager.SaveEncKeys("perm-test", rmpEnvelope, mmEnvelope)
	if err != nil {
		t.Fatalf("SaveEncKeys: %v", err)
	}
	for _, tier := range []string{"rmp", "mm"} {
		if info, err := os.Stat(filepath.Join(keyDir, tier)); err != nil {
			t.Fatalf("stat %s dir: %v", tier, err)
		} else if got := info.Mode().Perm(); got != config.DirPerm {
			t.Errorf("%s dir perm: got %#o, want %#o", tier, got, config.DirPerm)
		}
		if info, err := os.Stat(filepath.Join(keyDir, tier, "EncKey.json")); err != nil {
			t.Fatalf("stat %s/EncKey.json: %v", tier, err)
		} else if got := info.Mode().Perm(); got != config.FilePerm {
			t.Errorf("%s file perm: got %#o, want %#o", tier, got, config.FilePerm)
		}
	}
}

func TestSaveEncKeys_OverwritesExisting(t *testing.T) {
	withTempHome(t)

	if _, err := keymanager.SaveEncKeys("over", []byte("old-rmp"), []byte("old-mm")); err != nil {
		t.Fatalf("first SaveEncKeys: %v", err)
	}
	keyDir, err := keymanager.SaveEncKeys("over", []byte("new-rmp"), []byte("new-mm"))
	if err != nil {
		t.Fatalf("second SaveEncKeys: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(keyDir, "rmp", "EncKey.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(got) != "new-rmp" {
		t.Errorf("overwrite: got %q, want %q", got, "new-rmp")
	}
}

func TestKeyDir_ReturnsExpectedPath(t *testing.T) {
	home := withTempHome(t)

	got, err := keymanager.KeyDir("my-key")
	if err != nil {
		t.Fatalf("KeyDir: %v", err)
	}
	want := filepath.Join(home, ".rune", "keys", "my-key")
	if got != want {
		t.Errorf("KeyDir: got %q, want %q", got, want)
	}
}
