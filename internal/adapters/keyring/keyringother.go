//go:build !linux

package keyring

func detectAvailable() bool { return true } // Windows, macOS keychain
