package lifecycle

import (
	"bytes"
	"context"
	"testing"
	"time"
)

type recordCloser struct{ closed bool }

func (c *recordCloser) Close() error { c.closed = true; return nil }

// Exit sequence contract: adapters closed, DEKs zeroized, nil closers skipped.
func TestGracefulShutdown_ClosesAndZeroizes(t *testing.T) {
	a, b := &recordCloser{}, &recordCloser{}
	dek := []byte{1, 2, 3, 4}

	if err := GracefulShutdown(context.Background(), nil, []Closer{a, nil, b}, dek); err != nil {
		t.Fatalf("GracefulShutdown: %v", err)
	}
	if !a.closed || !b.closed {
		t.Fatalf("closers not closed: a=%v b=%v", a.closed, b.closed)
	}
	if !bytes.Equal(dek, []byte{0, 0, 0, 0}) {
		t.Fatalf("dek not zeroized: %v", dek)
	}
}

// Step 1 drain: an in-flight tool call holds shutdown until it ends.
func TestGracefulShutdown_DrainsInflight(t *testing.T) {
	tracker := NewInflightTracker()
	tracker.Begin()
	released := make(chan struct{})
	go func() {
		time.Sleep(80 * time.Millisecond)
		tracker.End()
		close(released)
	}()

	start := time.Now()
	if err := GracefulShutdown(context.Background(), tracker, nil); err != nil {
		t.Fatalf("GracefulShutdown: %v", err)
	}
	<-released
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond {
		t.Fatalf("shutdown returned before the in-flight call ended (%v)", elapsed)
	}
}
