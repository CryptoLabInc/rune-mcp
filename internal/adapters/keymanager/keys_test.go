package keymanager_test

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"sync"
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

// wantDirName restates the on-disk naming contract independently of the
// implementation: <keyID>@ + the first 24 hex characters of
// SHA-256(uint64be(len(rmp)) || rmp || mm).
func wantDirName(keyID string, rmp, mm []byte) string {
	h := sha256.New()
	var n [8]byte
	binary.BigEndian.PutUint64(n[:], uint64(len(rmp)))
	h.Write(n[:])
	h.Write(rmp)
	h.Write(mm)
	return keyID + "@" + hex.EncodeToString(h.Sum(nil))[:24]
}

func assertPair(t *testing.T, keyDir string, rmp, mm []byte) {
	t.Helper()
	for _, tc := range []struct {
		tier string
		want []byte
	}{
		{"rmp", rmp},
		{"mm", mm},
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

func TestSaveEncKeys_WritesBothTiersVerbatim(t *testing.T) {
	home := withTempHome(t)

	keyDir, err := keymanager.SaveEncKeys("key-test", rmpEnvelope, mmEnvelope)
	if err != nil {
		t.Fatalf("SaveEncKeys: %v", err)
	}
	if want := filepath.Join(home, ".rune", "keys", wantDirName("key-test", rmpEnvelope, mmEnvelope)); keyDir != want {
		t.Errorf("keyDir: got %q, want %q", keyDir, want)
	}

	// Each tier lands under its subdirectory as EncKey.json, byte-identical —
	// keymanager must NOT transform the envelope (the cgo unwrap parses it).
	assertPair(t, keyDir, rmpEnvelope, mmEnvelope)
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
	for _, dir := range []string{keyDir, filepath.Join(keyDir, "rmp"), filepath.Join(keyDir, "mm")} {
		if info, err := os.Stat(dir); err != nil {
			t.Fatalf("stat %s: %v", dir, err)
		} else if got := info.Mode().Perm(); got != config.DirPerm {
			t.Errorf("%s perm: got %#o, want %#o", dir, got, config.DirPerm)
		}
	}
	for _, tier := range []string{"rmp", "mm"} {
		if info, err := os.Stat(filepath.Join(keyDir, tier, "EncKey.json")); err != nil {
			t.Fatalf("stat %s/EncKey.json: %v", tier, err)
		} else if got := info.Mode().Perm(); got != config.FilePerm {
			t.Errorf("%s file perm: got %#o, want %#o", tier, got, config.FilePerm)
		}
	}
}

// TestSaveEncKeys_DistinctPairsKeepDistinctDirs is the regression for sessions
// that share ~/.rune/keys: a boot that receives another EncKey pair under the
// same key ID (every Console sends "rune-console-key") must not replace the
// pair an earlier boot saved, because that session reloads from its directory
// after idle.
func TestSaveEncKeys_DistinctPairsKeepDistinctDirs(t *testing.T) {
	withTempHome(t)

	dirA, err := keymanager.SaveEncKeys("rune-console-key", []byte("rmp-A"), []byte("mm-A"))
	if err != nil {
		t.Fatalf("SaveEncKeys(A): %v", err)
	}
	dirB, err := keymanager.SaveEncKeys("rune-console-key", []byte("rmp-B"), []byte("mm-B"))
	if err != nil {
		t.Fatalf("SaveEncKeys(B): %v", err)
	}
	if dirA == dirB {
		t.Fatalf("two EncKey pairs share one directory %q; the second boot replaced the first session's key", dirA)
	}
	assertPair(t, dirA, []byte("rmp-A"), []byte("mm-A"))
	assertPair(t, dirB, []byte("rmp-B"), []byte("mm-B"))

	// A pair that differs in one tier only still gets its own directory.
	dirMM, err := keymanager.SaveEncKeys("rune-console-key", []byte("rmp-A"), []byte("mm-B"))
	if err != nil {
		t.Fatalf("SaveEncKeys(rmp-A, mm-B): %v", err)
	}
	if dirMM == dirA || dirMM == dirB {
		t.Errorf("pair differing in one tier reused %q", dirMM)
	}
}

// TestSaveEncKeys_SamePairReusesDir: parallel sessions on one workspace receive
// identical bytes and share one directory, rewritten in place.
func TestSaveEncKeys_SamePairReusesDir(t *testing.T) {
	withTempHome(t)

	first, err := keymanager.SaveEncKeys("same", rmpEnvelope, mmEnvelope)
	if err != nil {
		t.Fatalf("first SaveEncKeys: %v", err)
	}
	second, err := keymanager.SaveEncKeys("same", rmpEnvelope, mmEnvelope)
	if err != nil {
		t.Fatalf("second SaveEncKeys: %v", err)
	}
	if first != second {
		t.Errorf("identical pair: got %q then %q, want one directory", first, second)
	}
	assertPair(t, second, rmpEnvelope, mmEnvelope)
}

// TestSaveEncKeys_DigestSeparatesTiers: moving bytes across the rmp/mm boundary
// changes the directory.
func TestSaveEncKeys_DigestSeparatesTiers(t *testing.T) {
	withTempHome(t)

	d1, err := keymanager.SaveEncKeys("k", []byte("ab"), []byte("c"))
	if err != nil {
		t.Fatalf("SaveEncKeys(ab, c): %v", err)
	}
	d2, err := keymanager.SaveEncKeys("k", []byte("a"), []byte("bc"))
	if err != nil {
		t.Fatalf("SaveEncKeys(a, bc): %v", err)
	}
	if d1 == d2 {
		t.Errorf("(ab, c) and (a, bc) share directory %q", d1)
	}
}

func TestSaveEncKeys_RejectsUnsafeKeyID(t *testing.T) {
	home := withTempHome(t)

	for _, id := range []string{"", ".", "..", "a/b", "../escape", `a\b`, "a\x00b", strings.Repeat("k", keymanager.MaxKeyIDLen+1)} {
		if dir, err := keymanager.SaveEncKeys(id, rmpEnvelope, mmEnvelope); err == nil {
			t.Errorf("key id %q: got dir %q, want error", id, dir)
		}
	}
	// Rejection happens before any filesystem write, so HOME stays empty.
	if entries, err := os.ReadDir(home); err != nil {
		t.Fatalf("read HOME: %v", err)
	} else if len(entries) != 0 {
		t.Errorf("rejected key ids created %d entries under HOME (first %q)", len(entries), entries[0].Name())
	}
}

// TestSaveEncKeys_AcceptsLongestKeyID: the longest allowed key ID still fits
// the file-name limit once the digest suffix is added.
func TestSaveEncKeys_AcceptsLongestKeyID(t *testing.T) {
	withTempHome(t)

	id := strings.Repeat("k", keymanager.MaxKeyIDLen)
	keyDir, err := keymanager.SaveEncKeys(id, rmpEnvelope, mmEnvelope)
	if err != nil {
		t.Fatalf("SaveEncKeys(%d-byte key id): %v", len(id), err)
	}
	if got := len(filepath.Base(keyDir)); got != 255 {
		t.Errorf("directory name is %d bytes, want 255", got)
	}
	assertPair(t, keyDir, rmpEnvelope, mmEnvelope)
}

// TestSaveEncKeys_ConcurrentBootsKeepEachPair models parallel sessions booting
// against two Consoles at once: each boot's directory must hold its own pair,
// including when read back while the other boots are still writing.
func TestSaveEncKeys_ConcurrentBootsKeepEachPair(t *testing.T) {
	withTempHome(t)

	pairs := [][2][]byte{
		{[]byte("rmp-console-A"), []byte("mm-console-A")},
		{[]byte("rmp-console-B"), []byte("mm-console-B")},
	}
	dirs := make([]string, 16)
	var wg sync.WaitGroup
	for i := range dirs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			p := pairs[i%2]
			dir, err := keymanager.SaveEncKeys("rune-console-key", p[0], p[1])
			if err != nil {
				t.Errorf("boot %d: SaveEncKeys: %v", i, err)
				return
			}
			dirs[i] = dir
			for _, tc := range []struct {
				tier string
				want []byte
			}{{"rmp", p[0]}, {"mm", p[1]}} {
				got, err := os.ReadFile(filepath.Join(dir, tc.tier, "EncKey.json"))
				if err != nil {
					t.Errorf("boot %d: read %s: %v", i, tc.tier, err)
					continue
				}
				if !bytes.Equal(got, tc.want) {
					t.Errorf("boot %d: %s holds %q, want %q", i, tc.tier, got, tc.want)
				}
			}
		}(i)
	}
	wg.Wait()
	for i, dir := range dirs {
		if dir != dirs[i%2] {
			t.Errorf("boot %d: dir %q, want %q (same pair, same dir)", i, dir, dirs[i%2])
		}
	}
	if dirs[0] == dirs[1] {
		t.Fatalf("both Consoles' pairs share %q", dirs[0])
	}
	assertPair(t, dirs[0], pairs[0][0], pairs[0][1])
	assertPair(t, dirs[1], pairs[1][0], pairs[1][1])
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
