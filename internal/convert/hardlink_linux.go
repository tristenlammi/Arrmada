//go:build linux

package convert

import "syscall"

// fileLinks returns the hardlink count for a path (1 = not hardlinked). A count > 1 means
// the file shares its inode — typically with a still-seeding torrent — so replacing it
// would break that link. Convert never edits in place, but by default it skips these so a
// convert doesn't silently duplicate a seeding file's disk usage.
func fileLinks(path string) uint64 {
	var st syscall.Stat_t
	if err := syscall.Stat(path, &st); err != nil {
		return 1
	}
	return uint64(st.Nlink)
}

// freeBytes returns the free space available at dir (0 if it can't be determined).
func freeBytes(dir string) uint64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0
	}
	return st.Bavail * uint64(st.Bsize)
}
