package mcp_test

import (
	"os"
	"testing"

	zk "github.com/zalando/go-keyring"
)

// TestMain swaps go-keyring's backend for its in-memory mock so tests that
// exercise the configure flow (LifecycleService.Configure → keyring.Set) never
// touch — and never prompt against — the host's real OS keychain.
func TestMain(m *testing.M) {
	zk.MockInit()
	os.Exit(m.Run())
}
