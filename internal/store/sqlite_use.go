package store

import (
	"errors"
	"os"
	"path/filepath"
)

// The stable sidecar is never unlinked: replacing a locked inode could admit a
// second process. Shared runtime/maintenance leases preserve existing SQLite
// access patterns; offline conversion takes an exclusive nonblocking lease.
func lockSQLiteUse(dir string, exclusive bool) (*os.File, error) {
	path := filepath.Join(dir, ".arrmada-sqlite-use.lock")
	if info, err := os.Lstat(path); err == nil && !info.Mode().IsRegular() {
		return nil, errors.New("SQLite use lock must be a regular application-owned file")
	} else if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := sqliteFileLock(f, exclusive); err != nil {
		_ = f.Close()
		return nil, err
	}
	return f, nil
}
