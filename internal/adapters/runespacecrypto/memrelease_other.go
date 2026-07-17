//go:build !darwin

package runespacecrypto

import "runtime/debug"

// releaseFreedMemory returns freed memory to the OS after the evi key set is
// unloaded. On non-darwin platforms only the Go heap is trimmed; the system
// allocator is left to its own reclaim policy (glibc arenas shrink via
// M_TRIM_THRESHOLD on free).
func releaseFreedMemory() {
	debug.FreeOSMemory()
}
