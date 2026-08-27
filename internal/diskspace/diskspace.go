// Package diskspace reports space on the filesystem holding a path. The
// real implementation is platform-specific (build-tagged); on unsupported
// platforms it reports "unknown" so callers can skip disk-based decisions rather
// than guess.
package diskspace

// Usage describes a filesystem's capacity. Free is what an unprivileged user can
// actually write, which on a reserved-block filesystem is less than Total-Used.
type Usage struct {
	TotalBytes uint64 `json:"total_bytes"`
	FreeBytes  uint64 `json:"free_bytes"`
	UsedBytes  uint64 `json:"used_bytes"`
	// UsedPct is 0-100, computed against the space visible to us (used+free)
	// rather than Total, so the bar matches the numbers printed beside it.
	UsedPct float64 `json:"used_pct"`
}

// Of returns the usage of the filesystem containing path, and whether it could be
// measured (false on platforms without support, or for a path that doesn't exist).
func Of(path string) (Usage, bool) {
	total, free, ok := stat(path)
	if !ok || total == 0 {
		return Usage{}, false
	}
	u := Usage{TotalBytes: total, FreeBytes: free}
	if free <= total {
		u.UsedBytes = total - free
	}
	if visible := u.UsedBytes + free; visible > 0 {
		u.UsedPct = float64(u.UsedBytes) / float64(visible) * 100
	}
	return u, true
}

// FreeGB returns the free space in GB on the filesystem containing path, and
// whether it could be measured (false on platforms without support).
func FreeGB(path string) (float64, bool) {
	u, ok := Of(path)
	if !ok {
		return 0, false
	}
	return float64(u.FreeBytes) / (1024 * 1024 * 1024), true
}
