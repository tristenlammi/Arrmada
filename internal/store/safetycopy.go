package store

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"time"

	"github.com/tristenlammi/arrmada/internal/diskspace"
)

// SafetyCopy writes a consistent copy of the live database to
// <dataDir>/backups/arrmada-<kind>-<UTC timestamp>.db before something rewrites data that
// can't be put back (deleting a user cascades to their audiobook places and listening
// history), then keeps only the newest `keep` copies of that kind. Other files in the
// backups folder are never touched.
//
// The copy is made with VACUUM INTO, so it includes rows still sitting in the WAL, and it
// is checked with quick_check before it gets its final name: a half-written or corrupt
// copy never looks like a usable backup.
func (s *Store) SafetyCopy(ctx context.Context, dataDir, kind string, keep int) (string, error) {
	if !safetyKind.MatchString(kind) {
		return "", fmt.Errorf("invalid backup kind %q", kind)
	}
	dir := filepath.Join(dataDir, "backups")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	if err := checkCopySpace(dataDir, dir); err != nil {
		return "", err
	}
	dst := filepath.Join(dir, fmt.Sprintf("arrmada-%s-%s.db", kind, time.Now().UTC().Format("20060102T150405Z")))
	tmp := dst + ".tmp"
	// VACUUM INTO refuses to write over an existing file, so clear a leftover from a crash.
	_ = os.Remove(tmp)
	fail := func(err error) (string, error) {
		_ = os.Remove(tmp)
		return "", err
	}
	if _, err := s.db.ExecContext(ctx, `VACUUM INTO ?`, tmp); err != nil {
		return fail(fmt.Errorf("copy database: %w", err))
	}
	if err := quickCheck(ctx, tmp); err != nil {
		return fail(err)
	}
	if err := fsyncFile(tmp); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, dst); err != nil {
		return fail(err)
	}
	pruneSafetyCopies(dir, kind, keep)
	return dst, nil
}

// safetyKind keeps the kind to a plain word so it can't steer the file name elsewhere.
var safetyKind = regexp.MustCompile(`^[a-z]+(-[a-z]+)*$`)

// checkCopySpace refuses when the backups disk clearly can't hold another copy of the
// database (with some headroom). Where free space can't be measured it doesn't block.
func checkCopySpace(dataDir, dir string) error {
	u, ok := diskspace.Of(dir)
	if !ok {
		return nil
	}
	var size int64
	for _, f := range []string{"arrmada.db", "arrmada.db-wal"} {
		if fi, err := os.Stat(filepath.Join(dataDir, f)); err == nil {
			size += fi.Size()
		}
	}
	need := uint64(float64(size) * 1.2)
	if u.FreeBytes < need {
		return fmt.Errorf("not enough free space in %s for a database copy (need %d MB, %d MB free)", dir, need>>20, u.FreeBytes>>20)
	}
	return nil
}

// quickCheck opens the copy read-only and requires SQLite to call it sound.
func quickCheck(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return fmt.Errorf("open copy: %w", err)
	}
	defer db.Close()
	var res string
	if err := db.QueryRowContext(ctx, `PRAGMA quick_check`).Scan(&res); err != nil {
		return fmt.Errorf("check copy: %w", err)
	}
	if res != "ok" {
		return fmt.Errorf("database copy failed its integrity check: %s", res)
	}
	return nil
}

func fsyncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	return f.Close()
}

// pruneSafetyCopies deletes all but the newest `keep` copies of one kind. The timestamp in
// the name sorts chronologically, so name order is age order.
func pruneSafetyCopies(dir, kind string, keep int) {
	if keep <= 0 {
		return
	}
	re := regexp.MustCompile(`^arrmada-` + regexp.QuoteMeta(kind) + `-\d{8}T\d{6}Z\.db$`)
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	var names []string
	for _, e := range ents {
		if !e.IsDir() && re.MatchString(e.Name()) {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for i := 0; i < len(names)-keep; i++ {
		_ = os.Remove(filepath.Join(dir, names[i]))
	}
}
