//go:build unix

package spawn

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// --- ResolveRuneBinary tests ---//
func TestResolveRuneBinary_RuneHomeWinsOverPluginRoot(t *testing.T) {
	runeHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runeHome, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	homeBin := filepath.Join(runeHome, "bin", "rune")
	if err := os.WriteFile(homeBin, []byte("home"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Verify precedence
	pluginRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pluginRoot, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pluginRoot, "bin", "rune"), []byte("plugin"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("RUNE_HOME", runeHome)
	t.Setenv("CLAUDE_PLUGIN_ROOT", pluginRoot)
	t.Setenv("PATH", "")

	got, err := ResolveRuneBinary()
	if err != nil {
		t.Fatalf("ResolveRuneBinary: %v", err)
	}
	if got != homeBin {
		t.Errorf("got %q, want %q (canonical install should win over plugin tree)", got, homeBin)
	}
}

func TestResolveRuneBinary_PluginRootFallback(t *testing.T) {
	emptyHome := t.TempDir()
	t.Setenv("RUNE_HOME", emptyHome) // no rune binary

	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	bin := filepath.Join(dir, "bin", "rune")
	if err := os.WriteFile(bin, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("CLAUDE_PLUGIN_ROOT", dir)
	t.Setenv("PATH", "")

	got, err := ResolveRuneBinary()
	if err != nil {
		t.Fatalf("ResolveRuneBinary: %v", err)
	}
	if got != bin {
		t.Errorf("got %q, want %q", got, bin)
	}
}

func TestResolveRuneBinary_NotFound(t *testing.T) {
	t.Setenv("RUNE_HOME", t.TempDir())
	t.Setenv("CLAUDE_PLUGIN_ROOT", "")
	t.Setenv("PATH", "")

	_, err := ResolveRuneBinary()
	if !errors.Is(err, ErrRuneBinaryNotFound) {
		t.Errorf("err: got %v, want ErrRuneBinaryNotFound", err)
	}
}

//--- Socket tests ---//

func startListener(t *testing.T, path string) {
	t.Helper()
	lis, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen %s: %v", path, err)
	}

	// Simulate reachable runed socket
	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()
	t.Cleanup(func() { _ = lis.Close() })
}

func TestProbeSocket_ReachableTrue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "embed.sock")
	startListener(t, path)

	if !probeSocket(path) {
		t.Errorf("probeSocket should return true for a live listener")
	}
}

func TestProbeSocket_NoListenerFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "embed.sock")
	if probeSocket(path) {
		t.Errorf("probeSocket should return false when socket file is absent")
	}
}

func TestWaitForSocket_ReturnsWhenListenerAppears(t *testing.T) {
	path := filepath.Join(t.TempDir(), "embed.sock")

	go func() {
		time.Sleep(100 * time.Millisecond)
		startListener(t, path)
	}()

	err := waitForSocket(context.Background(), path, time.Second, 50*time.Millisecond)
	if err != nil {
		t.Errorf("waitForSocket: %v, want nil (listener should appear within timeout)", err)
	}
}

func TestWaitForSocket_TimesOut(t *testing.T) {
	path := filepath.Join(t.TempDir(), "never.sock")

	start := time.Now()
	err := waitForSocket(context.Background(), path, 100*time.Millisecond, 30*time.Millisecond)
	elapsed := time.Since(start)
	if err == nil {
		t.Fatal("waitForSocket should timeout when no listener ever appears")
	}
	if !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("err: got %q, want substring 'not reachable'", err.Error())
	}

	if elapsed > 400*time.Millisecond {
		t.Errorf("waitForSocket took %v; expected ~100ms", elapsed)
	}
}

//--- EnsureDaemon tests ---//

func TestEnsureDaemon_AlreadyReachableSkipsSpawn(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "embed.sock")
	startListener(t, socketPath)

	cfg := Config{
		RuneBinary:    "/usr/bin/false", // exit non-zero
		SocketPath:    socketPath,
		SpawnLockPath: filepath.Join(t.TempDir(), "spawn.lock"),
		ReadyTimeout:  time.Second,
	}
	if err := EnsureDaemon(context.Background(), cfg); err != nil {
		t.Errorf("EnsureDaemon: %v, want nil for already-reachable socket", err)
	}
}

func TestEnsureDaemon_LockContentionSkipsExecPollsSocket(t *testing.T) {
	socketPath := filepath.Join(t.TempDir(), "embed.sock")
	lockPath := filepath.Join(t.TempDir(), "spawn.lock")

	// Simluate another mcp hold lock
	holder, locked, err := acquireSpawnLock(lockPath)
	if err != nil || !locked {
		t.Fatalf("priming lock: locked=%v err=%v", locked, err)
	}
	defer holder.Close()

	cfg := Config{
		RuneBinary:    "/usr/bin/false",
		SocketPath:    socketPath,
		SpawnLockPath: lockPath,
		ReadyTimeout:  150 * time.Millisecond,
		PollInterval:  30 * time.Millisecond,
	}
	err = EnsureDaemon(context.Background(), cfg)
	if err == nil {
		t.Fatal("expected timeout error (socket never comes up under contention)")
	}
	if !strings.Contains(err.Error(), "not reachable") {
		t.Errorf("err: got %q, want substring 'not reachable' (socket-poll timeout, not exec failure)", err.Error())
	}
}

//--- AgentInstallRecoveryHint tests ---//

func TestAgentInstallRecoveryHint_CanonicalExists(t *testing.T) {
	runeHome := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runeHome, "bin"), 0o755); err != nil {
		t.Fatal(err)
	}

	canonical := filepath.Join(runeHome, "bin", "rune")
	if err := os.WriteFile(canonical, []byte("fake"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("RUNE_HOME", runeHome)

	got := AgentInstallRecoveryHint()
	want := "`" + canonical + " install`"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestAgentInstallRecoveryHint_FallsBackToBootstrap(t *testing.T) {
	t.Setenv("RUNE_HOME", t.TempDir())

	got := AgentInstallRecoveryHint()
	want := "`bash -c \"${CLAUDE_PLUGIN_ROOT}/bin/rune install\"`"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
