// Package keyring stores the console session token in the OS-native secret
// store — macOS Keychain, Linux Secret Service (libsecret), or Windows
// Credential Manager — so the credential never sits in a plaintext file.
//
// It is a thin wrapper over github.com/zalando/go-keyring. Every call can fail
// on a host with no usable keyring (headless CI, no D-Bus, a locked keychain);
// callers detect that via IsUnavailable and fall back to config-file storage.

package keyring

import (
	"errors"
	"fmt"

	zk "github.com/zalando/go-keyring"
)

// service is the keyring service name all rune-mcp secrets live under. In the
// macOS Keychain / GNOME Seahorse UI the entry shows as this service + account.
const service = "rune-mcp"

// ErrUnavailable wraps any failure that means the OS keyring cannot be used
// (missing backend, no D-Bus session, locked/denied keychain). Callers should
// fall back to config-file storage rather than treating it as fatal.
var ErrUnavailable = errors.New("keyring unavailable")

var available = detectAvailable // detect whether OS keyring backend is present or not

// Set stores token for account (the console endpoint). A backend failure is
// returned wrapped in ErrUnavailable.
func Set(account, token string) error {
	if !available() {
		return fmt.Errorf("%w: no Secret service on this host", ErrUnavailable)
	}

	if err := zk.Set(service, account, token); err != nil {
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

// Get returns (token, true, nil) when present, ("", false, nil) when the
// account has no entry, and ("", false, ErrUnavailable-wrapped) when the keyring
// itself is unusable — a distinction callers need to tell "not stored here" from
// "cannot read the store".
func Get(account string) (string, bool, error) {
	if !available() {
		return "", false, fmt.Errorf("%w: no Secret service on this host", ErrUnavailable)
	}

	v, err := zk.Get(service, account)
	if errors.Is(err, zk.ErrNotFound) {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return v, true, nil
}

// Delete removes the account's entry. A missing entry is not an error.
func Delete(account string) error {
	if !available() {
		return nil
	}

	if err := zk.Delete(service, account); err != nil {
		if errors.Is(err, zk.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return nil
}

// IsUnavailable reports whether err signals a keyring the host cannot use.
func IsUnavailable(err error) bool { return errors.Is(err, ErrUnavailable) }
