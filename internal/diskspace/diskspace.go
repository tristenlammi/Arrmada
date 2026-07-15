// Package diskspace reports free space on the filesystem holding a path. The
// real implementation is platform-specific (build-tagged); on unsupported
// platforms it reports "unknown" so callers can skip disk-based decisions rather
// than guess.
package diskspace

// FreeGB returns the free space in GB on the filesystem containing path, and
// whether it could be measured (false on platforms without support).
func FreeGB(path string) (float64, bool) {
	b, ok := freeBytes(path)
	if !ok {
		return 0, false
	}
	return float64(b) / (1024 * 1024 * 1024), true
}
