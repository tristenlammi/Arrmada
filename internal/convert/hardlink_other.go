//go:build !linux

package convert

// fileLinks is a no-op on non-Linux hosts (hardlink counts aren't portably available);
// production runs in the Linux container where the real count is used.
func fileLinks(path string) uint64 { return 1 }

// freeBytes is unavailable off Linux; return max so the space guard never blocks on the
// dev host (production runs in the Linux container with a real check).
func freeBytes(dir string) uint64 { return 1 << 62 }
