package service

import (
	"bytes"
	"testing"
)

// Key cleanup must follow the last operation using it, including shutdown
// timeout paths; otherwise sealing races with a partially cleared metadata key.
func TestCaptureGenerationZeroizesAfterLastLease(t *testing.T) {
	s := NewCaptureService()
	s.SetDependencies(CaptureDependencies{AgentDEK: bytes.Repeat([]byte{7}, 32)})
	first, releaseFirst := s.acquireGeneration()
	_, releaseSecond := s.acquireGeneration()
	s.Shutdown()
	releaseFirst()
	if !bytes.Equal(first.AgentDEK, bytes.Repeat([]byte{7}, 32)) {
		t.Fatal("active generation key cleared before final capture returned")
	}
	releaseSecond()
	if !bytes.Equal(first.AgentDEK, make([]byte, 32)) {
		t.Fatal("retired generation key was not cleared after drain")
	}
}

type generationCloser struct{ calls int }

func (c *generationCloser) EncryptFlat([]float32) ([]byte, error)      { return nil, nil }
func (c *generationCloser) EncryptClustered([]float32) ([]byte, error) { return nil, nil }
func (c *generationCloser) Close() error                               { c.calls++; return nil }

func TestSharedAdapterRetiresAfterEveryGeneration(t *testing.T) {
	s := NewCaptureService()
	adapter := &generationCloser{}
	d := CaptureDependencies{Encryptor: adapter}
	s.SetDependencies(d)
	_, releaseFirst := s.acquireGeneration()
	s.AfterCaptureDrain(adapter, func() { _ = adapter.Close() })
	s.SetDependencies(d)
	_, releaseSecond := s.acquireGeneration()
	s.SetDependencies(CaptureDependencies{})
	releaseFirst()
	if adapter.calls != 0 {
		t.Fatal("shared adapter closed while newer generation still uses it")
	}
	releaseSecond()
	if adapter.calls != 1 {
		t.Fatalf("close calls=%d;want exactly 1", adapter.calls)
	}
}
