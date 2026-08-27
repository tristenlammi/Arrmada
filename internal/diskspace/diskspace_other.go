//go:build !linux

package diskspace

// stat is unsupported off Linux (e.g. Windows dev machines); callers treat
// "unknown" as "don't block".
func stat(string) (total, free uint64, ok bool) { return 0, 0, false }
