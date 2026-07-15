//go:build linux

package diskspace

import "syscall"

// freeBytes returns the space available to an unprivileged user on the
// filesystem containing path (the container's runtime OS).
func freeBytes(path string) (uint64, bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, false
	}
	return st.Bavail * uint64(st.Bsize), true
}
