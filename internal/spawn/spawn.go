// Runed auto-spawn coordinator
package spawn

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"time"
)

// Resolved by caller (LifecycleService)
type Config struct {
	RuneBinary    string // default: ~/.rune/bin/rune
	SocketPath    string // default: ~/.runed/embedding.sock
	SpawnLockPath string // default: ~/.runed/spawn.lock

	ReadyTimeout time.Duration // 0: DefaultReadyTimeout
	PollInterval time.Duration // 0: DefaultPollInterval
}

const (
	DefaultReadyTimeout = 15 * time.Second
	DefaultPollInterval = 500 * time.Millisecond
	probeDialTimeout    = 250 * time.Millisecond
)

var ErrRuneBinaryNotFound = errors.New("spawn: rune binary not found (run `rune install`)")

func ResolveRuneBinary() (string, error) {
	if path := defaultRuneBinaryPath(); path != "" { // $RUNE_HOME/bin/rune (commonly $HOME/.rune/bin/rune)
		if _, err := os.Stat(path); err == nil {
			return path, nil
		}
	}

	if root := os.Getenv("CLAUDE_PLUGIN_ROOT"); root != "" { // $CLAUDE_PLUGIN_ROOT/bin/rune
		candidate := filepath.Join(root, "bin", "rune")
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	if p, err := exec.LookPath("rune"); err == nil {
		return p, nil
	}

	return "", ErrRuneBinaryNotFound
}

func defaultRuneBinaryPath() string {
	if v := os.Getenv("RUNE_HOME"); v != "" {
		return filepath.Join(v, "bin", "rune")
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return ""
	}

	return filepath.Join(home, ".rune", "bin", "rune")
}

func AgentInstallRecoveryHint() string {
	if path := defaultRuneBinaryPath(); path != "" {
		if _, err := os.Stat(path); err == nil {
			return fmt.Sprintf("`%s install`", path)
		}
	}

	return "`bash -c \"${CLAUDE_PLUGIN_ROOT}/bin/rune install\"`" // very first install
}

func EnsureDaemon(ctx context.Context, cfg Config) error {
	cfg = applyDefaults(cfg)
	if cfg.RuneBinary == "" {
		return ErrRuneBinaryNotFound
	}
	if cfg.SocketPath == "" || cfg.SpawnLockPath == "" {
		return fmt.Errorf("spawn: SocketPath and SpawnLockPath must be set")
	}

	// Runed already reachable with socket
	if probeSocket(cfg.SocketPath) {
		return nil
	}

	// Try to acquire cfg.SpawnLockPath if not reachable
	lockFile, acquired, err := acquireSpawnLock(cfg.SpawnLockPath)
	if err != nil {
		return fmt.Errorf("spawn: lock: %w", err)
	}

	if acquired {
		defer lockFile.Close()
		// Double-check and spawn
		if !probeSocket(cfg.SocketPath) {
			if err := launchDaemon(ctx, cfg.RuneBinary); err != nil {
				return fmt.Errorf("spawn: exec `%s runed --detach`: %w", cfg.RuneBinary, err)
			}
		}
	}

	// Poll socket until reachable or timeout
	return waitForSocket(ctx, cfg.SocketPath, cfg.ReadyTimeout, cfg.PollInterval)
}

func launchDaemon(ctx context.Context, runeBin string) error {
	cmd := exec.CommandContext(ctx, runeBin, "runed", "--detach")
	// Discard launcher's output
	cmd.Stdout = nil
	cmd.Stderr = nil
	return cmd.Run()
}

func waitForSocket(ctx context.Context, socketPath string, timeout, poll time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if probeSocket(socketPath) {
			return nil
		}

		if !time.Now().Before(deadline) {
			return fmt.Errorf("spawn: socket %s not reachable after %s", socketPath, timeout)
		}

		select {
		case <-time.After(poll):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func probeSocket(path string) bool {
	conn, err := net.DialTimeout("unix", path, probeDialTimeout)
	if err != nil {
		return false
	}
	_ = conn.Close()

	return true
}

func applyDefaults(cfg Config) Config {
	if cfg.ReadyTimeout == 0 {
		cfg.ReadyTimeout = DefaultReadyTimeout
	}
	if cfg.PollInterval == 0 {
		cfg.PollInterval = DefaultPollInterval
	}

	return cfg
}
