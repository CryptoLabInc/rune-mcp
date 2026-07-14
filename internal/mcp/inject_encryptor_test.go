package mcp

import (
	"sync/atomic"
	"testing"
	"time"
)

// fakeEncryptor satisfies lifecycle.Encryptor and records Close.
type fakeEncryptor struct{ closed atomic.Bool }

func (f *fakeEncryptor) EncryptFlat([]float32) ([]byte, error)      { return nil, nil }
func (f *fakeEncryptor) EncryptClustered([]float32) ([]byte, error) { return nil, nil }
func (f *fakeEncryptor) Close() error                               { f.closed.Store(true); return nil }

// A replaced encryptor's cgo handle must be released (after the drain
// interval); the current one must stay open. Guards the re-boot leak:
// console restart / boot retry / reload_pipelines each re-open and inject.
func TestInjectEncryptor_ClosesReplacedHandle(t *testing.T) {
	prevInterval := staleClientCloseTime
	staleClientCloseTime = 10 * time.Millisecond
	t.Cleanup(func() { staleClientCloseTime = prevInterval })

	d := &Deps{}
	first := &fakeEncryptor{}
	second := &fakeEncryptor{}

	d.InjectEncryptor(first) // prev == nil — must not panic, nothing to close
	d.InjectEncryptor(second)

	deadline := time.Now().Add(2 * time.Second)
	for !first.closed.Load() {
		if time.Now().After(deadline) {
			t.Fatal("replaced encryptor was not closed within the drain interval")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if second.closed.Load() {
		t.Fatal("current encryptor must not be closed")
	}
	if d.Encryptor != second {
		t.Fatal("Deps.Encryptor should point at the new handle")
	}
}

// Re-injecting the same handle (idempotent boot outcome) must not close it.
func TestInjectEncryptor_SameHandleNotClosed(t *testing.T) {
	prevInterval := staleClientCloseTime
	staleClientCloseTime = 10 * time.Millisecond
	t.Cleanup(func() { staleClientCloseTime = prevInterval })

	d := &Deps{}
	enc := &fakeEncryptor{}
	d.InjectEncryptor(enc)
	d.InjectEncryptor(enc)

	time.Sleep(50 * time.Millisecond)
	if enc.closed.Load() {
		t.Fatal("re-injected identical handle must stay open")
	}
}
