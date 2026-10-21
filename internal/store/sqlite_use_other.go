//go:build !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package store

import (
	"errors"
	"os"
)

func sqliteFileLock(_ *os.File, exclusive bool) error {
	if exclusive {
		return errors.New("offline SQLite conversion requires OS file locking supported on Linux/BSD/macOS")
	}
	return nil // Keep existing SQLite runtime behavior on other platforms.
}

func sqliteSingleLink(os.FileInfo) bool { return false }
