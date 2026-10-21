//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package store

import (
	"os"
	"syscall"
)

func sqliteFileLock(f *os.File, exclusive bool) error {
	mode := syscall.LOCK_SH
	if exclusive {
		mode = syscall.LOCK_EX
	}
	if err := syscall.Flock(int(f.Fd()), mode|syscall.LOCK_NB); err != nil {
		if err == syscall.EWOULDBLOCK || err == syscall.EAGAIN {
			return ErrDatabaseInUse
		}
		return err
	}
	return nil
}

func sqliteSingleLink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}
