//go:build linux

package keyring

import (
	"sync"

	"github.com/godbus/dbus/v5"
)

const secretService = "org.freedesktop.secrets"

var (
	availOnce sync.Once
	availOK   bool
)

func detectAvailable() bool {
	availOnce.Do(func() { availOK = probeSecretService() })
	return availOK
}

func probeSecretService() bool {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return false // no session bus -> headless(no keyring)
	}
	defer conn.Close()

	// Cached per process
	var owner string
	err = conn.BusObject().Call(
		"org.freedesktop.DBus.GetNameOwner", 0, secretService,
	).Store(&owner)

	return err == nil && owner != ""
}
