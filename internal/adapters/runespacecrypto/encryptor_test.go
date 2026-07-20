package runespacecrypto

import (
	"strings"
	"sync"
	"testing"
	"time"

	runespace "github.com/CryptoLabInc/runespace-sdk"
)

// genKeys generates a real (dim-128) key set the adapter can open. The
// adapter is a thin wrapper over cgo key handles, so faking the SDK would
// test nothing — these tests exercise the real load/unload lifecycle.
func genKeys(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := runespace.GenerateKeys(
		runespace.WithKeyPath(dir),
		runespace.WithKeyID("t"),
		runespace.WithKeyDim(128),
	); err != nil {
		t.Fatalf("GenerateKeys: %v", err)
	}
	return dir
}

func waitUnloaded(t *testing.T, e *Encryptor, within time.Duration) {
	t.Helper()
	deadline := time.Now().Add(within)
	for e.loaded() {
		if time.Now().After(deadline) {
			t.Fatalf("key set still loaded after %v", within)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// TestLazyLifecycle covers the on-demand load path end to end: Open probes
// without staying resident, concurrent encrypts share one load (run with
// -race), the idle timer unloads, and a later call reloads.
func TestLazyLifecycle(t *testing.T) {
	dir := genKeys(t)
	t.Setenv("RUNE_ENCRYPTOR_IDLE", "150ms")
	e, err := Open(dir, "t", 128)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer e.Close()
	if e.loaded() {
		t.Fatal("key set resident right after Open — probe must release it")
	}

	vec := make([]float32, 128)
	var wg sync.WaitGroup
	for w := 0; w < 6; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 10; i++ {
				if _, err := e.EncryptFlat(vec); err != nil {
					t.Errorf("EncryptFlat: %v", err)
				}
				if _, err := e.EncryptClustered(vec); err != nil {
					t.Errorf("EncryptClustered: %v", err)
				}
			}
		}()
	}
	wg.Wait()
	if !e.loaded() {
		t.Fatal("key set not resident immediately after a burst")
	}

	waitUnloaded(t, e, 3*time.Second)

	if _, err := e.EncryptFlat(vec); err != nil {
		t.Fatalf("EncryptFlat after idle unload (reload path): %v", err)
	}
}

// TestPerCallUnload covers RUNE_ENCRYPTOR_IDLE=0: the key set is released as
// soon as the last in-flight call finishes.
func TestPerCallUnload(t *testing.T) {
	dir := genKeys(t)
	t.Setenv("RUNE_ENCRYPTOR_IDLE", "0s")
	e, err := Open(dir, "t", 128)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer e.Close()

	vec := make([]float32, 128)
	if _, err := e.EncryptFlat(vec); err != nil {
		t.Fatalf("EncryptFlat: %v", err)
	}
	if e.loaded() {
		t.Fatal("idle=0: key set still resident after the call returned")
	}
}

// TestCloseRaces covers Close semantics: concurrent encrypts racing Close
// either succeed or fail with the closed error (never panic or leave the key
// set resident), and encrypt after Close is rejected.
func TestCloseRaces(t *testing.T) {
	dir := genKeys(t)
	t.Setenv("RUNE_ENCRYPTOR_IDLE", "1h") // unload must come from Close, not idle
	e, err := Open(dir, "t", 128)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}

	vec := make([]float32, 128)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 8; i++ {
				if _, err := e.EncryptFlat(vec); err != nil && !strings.Contains(err.Error(), "closed") {
					t.Errorf("EncryptFlat: %v", err)
				}
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); _ = e.Close() }()
	wg.Wait()

	if e.loaded() {
		t.Fatal("key set resident after Close drained the in-flight calls")
	}
	if _, err := e.EncryptFlat(vec); err == nil {
		t.Fatal("EncryptFlat after Close must fail")
	}
}

// TestTimerAcquireRace hammers the acquire/idle-timer/free interplay with the
// call interval tuned near the idle deadline, so a timer regularly fires at the
// same moment a new acquire runs — the stale-fired-timer window. Under -race it
// asserts the invariant that matters: an encrypt never fails or faults because a
// stale timer freed a key set in use (it must transparently reload if needed),
// and Close at the end drains cleanly. Timing-dependent by nature; run with
// -count to widen coverage.
func TestTimerAcquireRace(t *testing.T) {
	dir := genKeys(t)
	t.Setenv("RUNE_ENCRYPTOR_IDLE", "2ms")
	e, err := Open(dir, "t", 128)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer e.Close()

	vec := make([]float32, 128)
	var wg sync.WaitGroup
	for w := 0; w < 4; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 60; i++ {
				if _, err := e.EncryptFlat(vec); err != nil {
					t.Errorf("EncryptFlat: %v", err) // a freed-in-use key would surface here
				}
				time.Sleep(2 * time.Millisecond) // ~= idle: land calls on the fire edge
			}
		}()
	}
	wg.Wait()
}
