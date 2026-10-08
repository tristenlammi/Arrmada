package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/tristenlammi/arrmada/internal/diskspace"
)

// ErrNoSpace means the disk holding the backups folder is too full for a copy of
// the database.
var ErrNoSpace = errors.New("not enough free space for a database snapshot")

// freeSpace is diskspace.Of, swappable so tests can simulate a full disk.
var freeSpace = diskspace.Of

// spaceHeadroom is how much more than the database (plus its WAL) must be free
// before a snapshot is attempted: the copy is usually smaller, but a disk that
// ends up 100% full takes the live database down with it.
const spaceHeadroom = 1.2

// Snapshot writes a consistent copy of the live database to the backups folder
// and returns its path. The copy is checked before it gets its final name, so a
// file that exists under a backup name is always a usable database.
func (s *Store) Snapshot(ctx context.Context, kind BackupKind) (string, error) {
	dir := BackupsDir(s.dataDir)
	at := time.Now()
	dst := filepath.Join(dir, BackupName(kind, at))
	// Names have one-second resolution; never replace an earlier copy taken in the
	// same second, since it may hold the only record of the state before it.
	for i := 0; i < 60; i++ {
		if _, err := os.Stat(dst); errors.Is(err, os.ErrNotExist) {
			break
		}
		at = at.Add(time.Second)
		dst = filepath.Join(dir, BackupName(kind, at))
	}
	if err := snapshotTo(ctx, s.db, s.dbPath, dst); err != nil {
		return "", err
	}
	return dst, nil
}

// snapshotTo copies the database behind db (living at dbPath) to dst with
// VACUUM INTO, which reads through the connection and so includes everything
// committed, even what is still only in the WAL.
func snapshotTo(ctx context.Context, db *sql.DB, dbPath, dst string) (err error) {
	dir := filepath.Dir(dst)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := checkSpace(dir, dbPath); err != nil {
		return err
	}

	tmp := dst + ".tmp"
	removeTmp := func() {
		for _, p := range []string{tmp, tmp + "-wal", tmp + "-shm", tmp + "-journal"} {
			_ = os.Remove(p)
		}
	}
	// VACUUM INTO refuses to write over an existing file, and a leftover .tmp is
	// only ever a copy that didn't finish.
	removeTmp()
	defer func() {
		if err != nil {
			removeTmp()
		}
	}()

	if _, err := db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return fmt.Errorf("copy database: %w", err)
	}
	if err := quickCheck(ctx, tmp); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		return fmt.Errorf("flush snapshot: %w", err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fmt.Errorf("name snapshot: %w", err)
	}
	syncDir(dir)
	return nil
}

// checkSpace refuses when the backups disk can't comfortably hold another copy.
// Where free space can't be measured (anything but Linux) it doesn't block.
func checkSpace(dir, dbPath string) error {
	u, ok := freeSpace(dir)
	if !ok {
		return nil
	}
	var size int64
	for _, p := range []string{dbPath, dbPath + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			size += fi.Size()
		}
	}
	need := uint64(float64(size) * spaceHeadroom)
	if u.FreeBytes < need {
		return fmt.Errorf("%w: needs about %s in %s, only %s free", ErrNoSpace, formatBytes(need), dir, formatBytes(u.FreeBytes))
	}
	return nil
}

// quickCheck opens the finished copy read-only and asks SQLite to vouch for it.
func quickCheck(ctx context.Context, path string) error {
	// immutable: nothing else has this file open, and it stops SQLite trying to
	// create -wal/-shm files beside a copy we only want to read.
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		return fmt.Errorf("open snapshot to check it: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	var res string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&res); err != nil {
		return fmt.Errorf("check snapshot: %w", err)
	}
	if res != "ok" {
		return fmt.Errorf("snapshot failed its integrity check: %s", res)
	}
	return nil
}

func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

// syncDir makes the rename durable. Best effort: some platforms can't open a
// directory for syncing, and the copy itself is already on disk.
func syncDir(dir string) {
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		_ = d.Close()
	}
}

// PruneBackups keeps the newest keep backups of kind in dir and deletes the rest,
// oldest first. Only files whose name ParseBackupName recognises as that kind are
// ever considered, so other kinds and anything else in the folder are left alone.
func PruneBackups(dir string, kind BackupKind, keep int) (removed []string, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	type backup struct {
		name string
		at   time.Time
	}
	var mine []backup
	for _, e := range entries {
		if !e.Type().IsRegular() {
			continue
		}
		k, at, ok := ParseBackupName(e.Name())
		if ok && k == kind {
			mine = append(mine, backup{e.Name(), at})
		}
	}
	if keep < 0 {
		keep = 0
	}
	if len(mine) <= keep {
		return nil, nil
	}
	sort.Slice(mine, func(i, j int) bool { return mine[i].at.After(mine[j].at) })
	for _, b := range mine[keep:] {
		if rerr := os.Remove(filepath.Join(dir, b.name)); rerr != nil {
			err = errors.Join(err, rerr)
			continue
		}
		removed = append(removed, b.name)
	}
	return removed, err
}

// formatBytes renders a size for a log line or error message.
func formatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(b)/float64(div), "KMGTPE"[exp])
}
