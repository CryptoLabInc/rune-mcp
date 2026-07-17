//go:build darwin

package runespacecrypto

/*
#include <malloc/malloc.h>
*/
import "C"
import "runtime/debug"

// releaseFreedMemory returns freed-but-dirty allocator pages to the OS after
// the evi key set is unloaded. The cgo contexts allocate ~150 MB through the
// system malloc; free() alone leaves tens of MB of dirty arena pages counted
// against the process footprint until memory pressure reclaims them.
// malloc_zone_pressure_relief performs that reclaim eagerly (measured: ~45 MB
// post-close footprint drops to ~10-12 MB), and FreeOSMemory does the same for
// the Go heap. Called only on the unload path — never per encrypt.
func releaseFreedMemory() {
	C.malloc_zone_pressure_relief(nil, 0)
	debug.FreeOSMemory()
}
