// Package runespacecrypto wraps the runespace-go-sdk key set for the one
// operation rune-mcp performs locally: FHE-encrypting an embedding with the
// PUBLIC EncKey before handing the ciphertext to the Console. This is the only
// cgo (libevi) surface in rune-mcp — keeping it in one package bounds the
// build/platform constraint (evi archives are arm64-only).
//
// The Console delivers the EncKey pair (RMP JSON envelope + MM raw key) in the
// agent manifest; keymanager persists them in the SDK's on-disk layout and
// Open loads them Enc-only (no SecKey — decryption stays in the Console).
//
// Memory: the opened key set costs ~150 MB of resident memory (two evi CKKS
// contexts + keypacks), which dwarfs the rest of the process (~15 MB). Since
// captures are bursty and OpenKeys is cheap (~25 ms warm), the key set is NOT
// held for the process lifetime: Open only validates the on-disk keys, and
// each encrypt call loads them on demand. An idle timer (default 60s,
// RUNE_ENCRYPTOR_IDLE to override, "0" to unload after every call) releases
// the cgo handles so an idle rune-mcp returns to its small footprint.
package runespacecrypto

import (
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	runespace "github.com/CryptoLabInc/runespace-sdk"
)

// defaultIdle is how long the loaded key set is kept after the last encrypt
// before its cgo handles are released. Long enough that a burst (reseed, batch
// capture) never thrashes reopen, short enough that a chat-paced session
// spends most of its life at the small footprint.
const defaultIdle = 60 * time.Second

// idleFromEnv reads RUNE_ENCRYPTOR_IDLE ("30s", "2m", "0" — 0 unloads after
// every call). Unset or unparsable falls back to defaultIdle.
func idleFromEnv() time.Duration {
	v := os.Getenv("RUNE_ENCRYPTOR_IDLE")
	if v == "" {
		return defaultIdle
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		slog.Warn("runespacecrypto: bad RUNE_ENCRYPTOR_IDLE, using default", "value", v, "default", defaultIdle)
		return defaultIdle
	}
	return d
}

// Encryptor is the process-wide encrypt handle. The underlying key set is
// loaded lazily on first use and unloaded after the idle timeout; in-flight
// calls hold a refcount so the unload never races an encrypt. Safe for
// concurrent use: loads/unloads are serialized here, and the SDK serializes
// the cgo work per key bundle.
type Encryptor struct {
	keyDir string
	keyID  string
	dim    int
	idle   time.Duration

	mu       sync.Mutex
	keys     *runespace.Keys // nil while unloaded
	inflight int
	timer    *time.Timer
	closed   bool
}

// Open validates the EncKey pair in keyDir (the SDK layout keymanager wrote)
// for the given key id and dimension, requesting only the Enc part — this
// side never decrypts. The validation open is released immediately; the key
// set is reloaded on demand by the encrypt calls.
func Open(keyDir, keyID string, dim int) (*Encryptor, error) {
	keys, err := openKeys(keyDir, keyID, dim)
	if err != nil {
		return nil, err
	}
	closeAndRelease(keys) // probe only — stay at the small footprint until first use
	return &Encryptor{keyDir: keyDir, keyID: keyID, dim: dim, idle: idleFromEnv()}, nil
}

// closeAndRelease frees a key set's cgo handles and returns the freed pages to
// the OS. The two are always paired: closing alone leaves ~45 MB of dirty
// allocator arenas charged to the process until pressure reclaims them, so the
// memory win only lands once releaseFreedMemory runs. Keeping them in one
// function makes that invariant impossible to half-apply.
func closeAndRelease(keys *runespace.Keys) {
	_ = keys.Close()
	releaseFreedMemory()
}

func openKeys(keyDir, keyID string, dim int) (*runespace.Keys, error) {
	keys, err := runespace.OpenKeys(
		runespace.WithKeyPath(keyDir),
		runespace.WithKeyID(keyID),
		runespace.WithKeyDim(dim),
		runespace.WithKeyParts(runespace.KeyPartEnc),
	)
	if err != nil {
		return nil, fmt.Errorf("runespacecrypto: open keys: %w", err)
	}
	return keys, nil
}

// Dim reports the FHE slot dimension the key set was opened with.
func (e *Encryptor) Dim() int { return e.dim }

// loaded reports whether the key set is currently resident. Test hook for the
// lazy-lifecycle assertions; not part of the lifecycle.Encryptor surface.
func (e *Encryptor) loaded() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.keys != nil
}

// acquire returns the loaded key set and a release func. It loads the keys if
// they are unloaded, and pins them (refcount) so no unload can free them
// mid-call. When the last call releases, release either arms the idle timer
// (idle>0), unloads immediately (idle<=0), or unloads because Close is
// pending — always detaching under the lock and freeing off it.
func (e *Encryptor) acquire() (*runespace.Keys, func(), error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return nil, nil, fmt.Errorf("runespacecrypto: encryptor is closed")
	}
	if e.keys == nil {
		t0 := time.Now()
		keys, err := openKeys(e.keyDir, e.keyID, e.dim)
		if err != nil {
			return nil, nil, err
		}
		e.keys = keys
		slog.Info("runespacecrypto: key set loaded on demand", "took", time.Since(t0).Round(time.Millisecond).String())
	}
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	e.inflight++
	keys := e.keys
	release := func() {
		e.mu.Lock()
		e.inflight--
		var old *runespace.Keys
		why := ""
		if e.inflight == 0 && e.keys != nil {
			switch {
			case e.closed: // Close raced an in-flight call; we are the last out
				old, why = e.detachLocked(), "close"
			case e.idle <= 0: // unload after every call
				old, why = e.detachLocked(), "after call"
			default:
				e.armUnloadLocked() // schedule the idle timer
			}
		}
		e.mu.Unlock()
		freeDetached(old, why) // off-lock; no-op when old == nil
	}
	return keys, release, nil
}

// armUnloadLocked schedules the idle-unload timer. Caller holds mu and has
// already established idle > 0. The timer detaches under mu but frees the key
// set off-lock (see freeDetached) so the cgo close never runs under mu.
func (e *Encryptor) armUnloadLocked() {
	var t *time.Timer
	t = time.AfterFunc(e.idle, func() {
		e.mu.Lock()
		var old *runespace.Keys
		// Fire only if we are still the reigning timer. If this timer already
		// triggered but a racing acquire won the lock first, that acquire's
		// Stop() returned false yet still cleared/replaced e.timer (and reset
		// the idle clock). Without this identity check the stale timer would
		// unload on the OLD schedule after a fresh use — safe (the inflight==0
		// guard still prevents freeing keys in use) but an early unload that
		// ignores the reset.
		if e.timer == t && e.inflight == 0 && !e.closed {
			old = e.detachLocked()
			e.timer = nil
		}
		e.mu.Unlock()
		freeDetached(old, "idle "+e.idle.String())
	})
	e.timer = t
}

// detachLocked removes the resident key set and returns it (nil if none).
// Caller holds mu. The returned handle is unreachable by any other goroutine,
// so the caller frees it AFTER unlocking (freeDetached) — the cgo close + STW
// trim then never run while mu is held.
func (e *Encryptor) detachLocked() *runespace.Keys {
	k := e.keys
	e.keys = nil
	return k
}

// freeDetached closes an already-detached key set off-lock and returns its
// memory to the OS. Safe with a nil handle (the common no-op case).
func freeDetached(k *runespace.Keys, why string) {
	if k == nil {
		return
	}
	closeAndRelease(k)
	slog.Info("runespacecrypto: key set unloaded", "why", why)
}

// EncryptFlat produces the flat (RMP) tier ITEM ciphertext. vec must be
// l2-normalized and length Dim().
func (e *Encryptor) EncryptFlat(vec []float32) ([]byte, error) {
	if e == nil {
		return nil, fmt.Errorf("runespacecrypto: encryptor is closed")
	}
	keys, release, err := e.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	b, err := keys.EncryptFlat(vec)
	if err != nil {
		return nil, fmt.Errorf("runespacecrypto: encrypt flat: %w", err)
	}
	return b, nil
}

// EncryptClustered produces the compact cluster (MM) tier ITEM ciphertext.
// Same input contract as EncryptFlat.
func (e *Encryptor) EncryptClustered(vec []float32) ([]byte, error) {
	if e == nil {
		return nil, fmt.Errorf("runespacecrypto: encryptor is closed")
	}
	keys, release, err := e.acquire()
	if err != nil {
		return nil, err
	}
	defer release()
	b, err := keys.EncryptClustered(vec)
	if err != nil {
		return nil, fmt.Errorf("runespacecrypto: encrypt clustered: %w", err)
	}
	return b, nil
}

// Close releases the cgo key handles and rejects further use. Idempotent.
// In-flight encrypts finish on their pinned key set; the last release finds
// closed=true and skips re-arming, and Close itself frees the set if no call
// is in flight.
func (e *Encryptor) Close() error {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	e.closed = true
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	// Detach under the lock, free after unlocking (same off-lock discipline as
	// the idle/per-call paths). If a call is in flight we detach nothing; the
	// last release() sees closed==true and frees then.
	var old *runespace.Keys
	if e.inflight == 0 {
		old = e.detachLocked()
	}
	e.mu.Unlock()
	freeDetached(old, "close")
	return nil
}
