// Package logio handles ~/.rune/capture_log.jsonl (0600, append-only).
//
// Concurrency:
//   - Intra-process: sync.Mutex
//   - Inter-process: syscall.Flock(LOCK_EX)
//
// Failure policy: append failure → slog error, capture still succeeds.
package logio

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"syscall"

	"github.com/CryptoLabInc/rune-mcp/internal/domain"
)

// Path — ~/.rune/capture_log.jsonl.
const DefaultFilename = "capture_log.jsonl"

// CaptureLog — append handle.
type CaptureLog struct {
	path string
	mu   sync.Mutex
}

func New(path string) *CaptureLog {
	return &CaptureLog{path: path}
}

// Append — one JSONL line. Atomic (flock + O_APPEND + fsync).
//
// Steps:
//  1. sync.Mutex lock
//  2. os.OpenFile(O_APPEND | O_CREAT | O_WRONLY, 0600)
//  3. syscall.Flock(LOCK_EX) (inter-process)
//  4. json.Marshal(entry) + "\n" write + fsync
//  5. Flock(LOCK_UN) + close
//  6. Any error: log + return
func (l *CaptureLog) Append(entry domain.CaptureLogEntry) error {
	l.mu.Lock()
	defer l.mu.Unlock()

	f, err := os.OpenFile(l.path, os.O_WRONLY|os.O_CREATE|os.O_APPEND, 0600)
	if err != nil {
		slog.Error("capture log: open failed", "path", l.path, "err", err)
		return fmt.Errorf("capture log open: %w", err)
	}
	defer f.Close()

	// Inter-process lock
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		slog.Error("capture log: flock failed", "err", err)
		return fmt.Errorf("capture log flock: %w", err)
	}
	defer func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN) }()

	data, err := json.Marshal(entry)
	if err != nil {
		slog.Error("capture log: marshal failed", "err", err)
		return fmt.Errorf("capture log marshal: %w", err)
	}

	data = append(data, '\n')
	if _, err := f.Write(data); err != nil {
		slog.Error("capture log: write failed", "err", err)
		return fmt.Errorf("capture log write: %w", err)
	}

	if err := f.Sync(); err != nil {
		slog.Error("capture log: fsync failed", "err", err)
		return fmt.Errorf("capture log fsync: %w", err)
	}

	return nil
}
