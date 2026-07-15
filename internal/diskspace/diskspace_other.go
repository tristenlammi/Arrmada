//go:build !linux

package diskspace

// freeBytes is unsupported off Linux (e.g. Windows dev machines); callers treat
// "unknown" as "don't block".
func freeBytes(path string) (uint64, bool) { return 0, false }
