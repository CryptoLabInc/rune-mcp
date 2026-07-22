package keyring

import (
	"errors"
	"os"
	"testing"
	"time"

	zk "github.com/zalando/go-keyring"
)

func TestNoKeyringFallBack(t *testing.T) {
	zk.MockInit() // a backend exists, but availability=false
	available = func() bool { return false }
	t.Cleanup(func() { available = detectAvailable })

	if err := Set("acct", "evt_secret"); !IsUnavailable(err) {
		t.Fatalf("Set = %v; want ErrUnavailable", err)
	}
	if v, found, err := Get("acct"); found || v != "" || !IsUnavailable(err) {
		t.Fatalf("Get = (%q, %v, %v); want (\"\", false, ErrUnavailable)", v, found, err)
	}
	if err := Delete("acct"); err != nil {
		t.Fatalf("Delete = %v; want nil (no-op when no keyring)", err)
	}
	if _, err := zk.Get(service, "acct"); !errors.Is(err, zk.ErrNotFound) {
		t.Fatalf("backend was touched: zk.Get err = %v; want ErrNotFound", err)
	}
}
