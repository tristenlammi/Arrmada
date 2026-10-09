//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// becomeDataOwner switches a command run as root to the user and group that own the
// database. `docker exec Arrmada-app arrmada …` runs as root unless given -u, and the
// -wal/-shm files SQLite creates, or a backup, would then be root's: the app, running
// as PUID:PGID, couldn't write to its own database any more. It must run before any
// file in the data folder is opened. A database that root owns, or none at all, leaves
// the process as it is: there's no other owner to become, and nothing to protect.
func becomeDataOwner(dataDir string) error {
	if os.Geteuid() != 0 {
		return nil
	}
	var st syscall.Stat_t
	if err := syscall.Stat(filepath.Join(dataDir, "arrmada.db"), &st); err != nil {
		if err := syscall.Stat(dataDir, &st); err != nil {
			return nil
		}
	}
	if st.Uid == 0 {
		return nil
	}
	uid, gid := int(st.Uid), int(st.Gid)
	// Groups first: once the user changes, root's right to change them is gone.
	if err := syscall.Setgroups([]int{gid}); err != nil {
		return fmt.Errorf("switch to the database owner's groups (%d): %w", gid, err)
	}
	if err := syscall.Setgid(gid); err != nil {
		return fmt.Errorf("switch to the database owner's group (%d): %w", gid, err)
	}
	if err := syscall.Setuid(uid); err != nil {
		return fmt.Errorf("switch to the database owner (%d): %w", uid, err)
	}
	return nil
}
