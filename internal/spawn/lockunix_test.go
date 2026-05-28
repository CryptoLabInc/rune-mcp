//go:build unix

package spawn

import (
	"path/filepath"
	"testing"
)

func TestAcquireSpawnLock_FirstCallerWins(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spawn.lock")

	// First attempt (success)
	f1, locked1, err := acquireSpawnLock(path)
	if err != nil || !locked1 {
		t.Fatalf("first acquire: locked=%v err=%v", locked1, err)
	}
	t.Cleanup(func() { _ = f1.Close() })

	// Second attempt (fail)
	f2, locked2, err := acquireSpawnLock(path)
	if err != nil {
		t.Fatalf("second acquire: %v", err)
	}
	if locked2 {
		t.Error("second acquire should fail (lock held)")
		_ = f2.Close()
	}
	if f2 != nil {
		t.Error("contended acquire should return nil file handle")
	}
}

func TestAcquireSpawnLock_ReleasedOnClose(t *testing.T) {
	path := filepath.Join(t.TempDir(), "spawn.lock")

	// First attempt and close (success)
	f1, locked, err := acquireSpawnLock(path)
	if err != nil || !locked {
		t.Fatalf("first acquire: locked=%v err=%v", locked, err)
	}
	if err := f1.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	// Second attempt (success)
	f2, locked2, err := acquireSpawnLock(path)
	if err != nil {
		t.Fatalf("re-acquire: %v", err)
	}
	if !locked2 {
		t.Fatal("re-acquire after close should succeed")
	}
	_ = f2.Close()
}

func TestAcquireSpawnLock_CreatesParentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "deeper", "spawn.lock")

	f, locked, err := acquireSpawnLock(path)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if !locked {
		t.Fatal("acquire should succeed even with missing parent dir")
	}
	_ = f.Close()
}
