package keyring

import (
	"testing"

	zk "github.com/zalando/go-keyring"
)

// TestRoundTrip exercises Set/Get/Delete against the in-memory mock backend so
// the test never touches the host's real keychain.
func TestRoundTrip(t *testing.T) {
	zk.MockInit()
	available = func() bool { return true }
	t.Cleanup(func() { available = detectAvailable })

	const account = "127.0.0.1:50051"

	// Missing entry: found=false, no error.
	if _, found, err := Get(account); err != nil || found {
		t.Fatalf("Get(missing) = found=%v err=%v, want found=false err=nil", found, err)
	}

	if err := Set(account, "evt_secret"); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got, found, err := Get(account)
	if err != nil || !found || got != "evt_secret" {
		t.Fatalf("Get = (%q, %v, %v), want (\"evt_secret\", true, nil)", got, found, err)
	}

	if err := Delete(account); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, found, _ := Get(account); found {
		t.Fatal("Get after Delete still found the entry")
	}
	// Delete of a missing entry is not an error.
	if err := Delete(account); err != nil {
		t.Fatalf("Delete(missing) = %v, want nil", err)
	}
}
