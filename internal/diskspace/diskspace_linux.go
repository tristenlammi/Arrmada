//go:build linux

package diskspace

import "syscall"

// stat returns the total size of the filesystem containing path and the space
// available to an unprivileged user on it (the container's runtime OS).
func stat(path string) (total, free uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize)
	// Blocks is the whole filesystem; Bavail excludes the root-reserved blocks, so
	// it is what we can really write. Deliberately not Bfree.
	return st.Blocks * bs, st.Bavail * bs, true
}
