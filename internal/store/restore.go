package store

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Restoring a backup is never a live swap: the running app has the database open, and
// the scheduler writes to it at any moment. Instead a restore is validated and staged
// (a small marker file next to the database), and the swap happens at the next start,
// before anything opens the database:
//
//  1. re-validate the chosen backup,
//  2. copy the current database aside as a pre-restore backup,
//  3. copy the backup to arrmada.db.restore-tmp and flush it,
//  4. drop the old -wal/-shm/-journal files, which belong to the old database,
//  5. rename the copy over arrmada.db.
//
// The outcome is written to restore-result.json for the Backups card. Any failure before
// the rename leaves the original database in place and the app boots on it as usual.
// Normal migrations then bring an older backup forward (taking their own pre-migrate copy).

const (
	restorePendingFile = "restore-pending.json"
	restoreResultFile  = "restore-result.json"
	preRestoreKeep     = 3
)

var (
	// ErrBadBackupName is a name that isn't exactly one of our backup files (a path,
	// "..", someone's own file in the folder).
	ErrBadBackupName = errors.New("not a backup name")
	// ErrNotSQLite means the file doesn't start with SQLite's header.
	ErrNotSQLite = errors.New("not a SQLite database")
	// ErrCorrupt means SQLite's integrity check didn't pass.
	ErrCorrupt = errors.New("the database is damaged")
	// ErrNotArrmada means a sound SQLite file that isn't an Arrmada database.
	ErrNotArrmada = errors.New("not an Arrmada database")
	// ErrTooLarge means an imported backup is over the size it may grow to.
	ErrTooLarge = errors.New("the backup is too large")
)

// sqliteMagic is the first 16 bytes of every SQLite database file.
var sqliteMagic = []byte("SQLite format 3\x00")

// BackupInfo is what validation learnt about a backup.
type BackupInfo struct {
	SchemaVersion string   `json:"schema_version"`    // newest migration it holds
	Unknown       []string `json:"unknown,omitempty"` // applied versions this build doesn't have
}

// EmbeddedVersions lists the migration versions built into this binary, oldest first.
func EmbeddedVersions() []string { return versionsIn(embeddedMigrations()) }

func versionsIn(fsys fs.FS) []string {
	names, err := listMigrations(fsys)
	if err != nil {
		return nil
	}
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = strings.TrimSuffix(n, ".sql")
	}
	return out
}

// ValidateBackup checks that the file at path is a sound Arrmada database this build can
// run on: SQLite's header, a full integrity_check, the users and schema_migrations tables,
// and no applied migration missing from embedded. It opens the file read-only and never
// reads anything but those tables' names and the migration versions.
func ValidateBackup(path string, embedded []string) (BackupInfo, error) {
	var info BackupInfo
	f, err := os.Open(path)
	if err != nil {
		return info, err
	}
	head := make([]byte, len(sqliteMagic))
	_, err = io.ReadFull(f, head)
	_ = f.Close()
	if err != nil || !bytes.Equal(head, sqliteMagic) {
		return info, ErrNotSQLite
	}

	// immutable: nothing else has this copy open, and it stops SQLite creating -wal/-shm
	// files beside a file we only want to read.
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro&immutable=1")
	if err != nil {
		return info, fmt.Errorf("open backup: %w", err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	// No deadline: a full integrity check of a large database takes a while, and this is
	// the step that tells a restorable copy from a damaged one.
	ctx := context.Background()

	problems, err := integrityCheck(ctx, db)
	if err != nil {
		return info, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if len(problems) > 0 {
		return info, fmt.Errorf("%w: %s", ErrCorrupt, strings.Join(problems, "; "))
	}

	var tables int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name IN ('users','schema_migrations')`).Scan(&tables); err != nil {
		return info, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if tables != 2 {
		return info, fmt.Errorf("%w: it has no users or schema_migrations table", ErrNotArrmada)
	}

	rows, err := db.QueryContext(ctx, `SELECT version FROM schema_migrations`)
	if err != nil {
		return info, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	defer rows.Close()
	known := make(map[string]bool, len(embedded))
	for _, v := range embedded {
		known[v] = true
	}
	for rows.Next() {
		var v string
		if err := rows.Scan(&v); err != nil {
			return info, fmt.Errorf("%w: %v", ErrCorrupt, err)
		}
		if v > info.SchemaVersion {
			info.SchemaVersion = v
		}
		if !known[v] {
			info.Unknown = append(info.Unknown, v)
		}
	}
	if err := rows.Err(); err != nil {
		return info, fmt.Errorf("%w: %v", ErrCorrupt, err)
	}
	if len(info.Unknown) > 0 {
		sort.Strings(info.Unknown)
		return info, backupNewerError{unknown: info.Unknown}
	}
	return info, nil
}

// integrityCheck returns the first few problems PRAGMA integrity_check reports (none
// when it says "ok").
func integrityCheck(ctx context.Context, db *sql.DB) ([]string, error) {
	rows, err := db.QueryContext(ctx, `PRAGMA integrity_check(5)`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		if s != "ok" {
			out = append(out, s)
		}
	}
	return out, rows.Err()
}

// RestoreMarker is restore-pending.json: the backup to put in place at the next start.
type RestoreMarker struct {
	File        string    `json:"file"` // "backups/<name>", relative to the data dir
	RequestedBy string    `json:"requested_by"`
	At          time.Time `json:"at"`
}

// Name is the backup's file name, or "" when the marker doesn't point at exactly one of
// our backups (a hand-edited marker can't steer the restore anywhere else).
func (m RestoreMarker) Name() string {
	dir, name := path.Split(filepath.ToSlash(m.File))
	if dir != "backups/" {
		return ""
	}
	if _, _, ok := ParseBackupName(name); !ok {
		return ""
	}
	return name
}

// RestoreResult is restore-result.json: how the last restore at boot went.
type RestoreResult struct {
	At         time.Time `json:"at"`
	OK         bool      `json:"ok"`
	From       string    `json:"from,omitempty"`        // the backup restored (or tried)
	PreRestore string    `json:"pre_restore,omitempty"` // the copy of the database it replaced
	Error      string    `json:"error,omitempty"`
}

// ValidBackupName reports whether name is exactly one of our backup file names.
func ValidBackupName(name string) bool {
	if name == "" || filepath.Base(name) != name || path.Base(name) != name {
		return false
	}
	_, _, ok := ParseBackupName(name)
	return ok
}

// StageRestore validates the backup called name in <dataDir>/backups and, when it's
// sound, writes the marker that puts it in place at the next start. Nothing is staged
// for a backup that fails validation.
func StageRestore(dataDir, name, requestedBy string) (BackupInfo, error) {
	return stageRestore(dataDir, name, requestedBy, EmbeddedVersions())
}

func stageRestore(dataDir, name, requestedBy string, embedded []string) (BackupInfo, error) {
	if !ValidBackupName(name) {
		return BackupInfo{}, ErrBadBackupName
	}
	src := filepath.Join(BackupsDir(dataDir), name)
	if _, err := os.Stat(src); err != nil {
		return BackupInfo{}, err
	}
	info, err := ValidateBackup(src, embedded)
	if err != nil {
		return info, err
	}
	m := RestoreMarker{File: "backups/" + name, RequestedBy: requestedBy, At: time.Now().UTC()}
	if err := writeJSONFile(filepath.Join(dataDir, restorePendingFile), m); err != nil {
		return info, fmt.Errorf("stage the restore: %w", err)
	}
	return info, nil
}

// CancelRestore removes a staged restore that hasn't run yet. It reports whether there was one.
func CancelRestore(dataDir string) (bool, error) {
	err := os.Remove(filepath.Join(dataDir, restorePendingFile))
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	return err == nil, err
}

// PendingRestore returns the staged restore, or nil when there is none.
func PendingRestore(dataDir string) (*RestoreMarker, error) {
	var m RestoreMarker
	ok, err := readJSONFile(filepath.Join(dataDir, restorePendingFile), &m)
	if !ok || err != nil {
		return nil, err
	}
	return &m, nil
}

// LastRestore returns how the last restore at boot went, or nil when none has run.
func LastRestore(dataDir string) (*RestoreResult, error) {
	var r RestoreResult
	ok, err := readJSONFile(filepath.Join(dataDir, restoreResultFile), &r)
	if !ok || err != nil {
		return nil, err
	}
	return &r, nil
}

// pendingRestoreName is the backup a staged restore points at ("" when none), so pruning
// and deleting can leave it alone until it has run.
func pendingRestoreName(dataDir string) string {
	m, err := PendingRestore(dataDir)
	if err != nil || m == nil {
		return ""
	}
	return m.Name()
}

// applyPendingRestore runs at the top of OpenWith, before the database is opened, and
// puts a staged backup in place. It never stops the boot: whatever happens, the app
// starts on a whole database, and restore-result.json says which one.
func applyPendingRestore(dataDir string, embedded []string, log *slog.Logger) {
	m, err := PendingRestore(dataDir)
	if err == nil && m == nil {
		return
	}
	res := RestoreResult{}
	if err == nil {
		res.From = m.Name()
		if res.From == "" {
			res.From = m.File
		}
		log.Info("restoring the database from a backup", "backup", res.From, "requested_by", m.RequestedBy)
		res.PreRestore, err = restoreNow(dataDir, m, embedded, log)
	} else {
		err = fmt.Errorf("unreadable %s: %w", restorePendingFile, err)
	}
	res.At = time.Now().UTC()
	if err != nil {
		res.Error = err.Error()
		// The marker may still be there if the failure came before it was consumed.
		_ = os.Remove(filepath.Join(dataDir, restorePendingFile))
		log.Error("database restore failed; starting on the database as it was", "backup", res.From, "err", err)
	} else {
		res.OK = true
		log.Info("database restored from a backup", "backup", res.From, "pre_restore", res.PreRestore)
	}
	if werr := writeJSONFile(filepath.Join(dataDir, restoreResultFile), res); werr != nil {
		log.Warn("couldn't record the restore result", "err", werr)
	}
}

// restoreNow does the swap described at the top of this file and returns the name of
// the pre-restore copy ("" when there was no database to copy).
func restoreNow(dataDir string, m *RestoreMarker, embedded []string, log *slog.Logger) (string, error) {
	name := m.Name()
	if name == "" {
		return "", fmt.Errorf("the staged restore points at %q, which isn't a backup in the backups folder", m.File)
	}
	// Consume the marker before anything changes: if this boot dies halfway, or the
	// marker can't be removed afterwards, the restore must not run again at every start
	// and throw away whatever happened since.
	if err := os.Remove(filepath.Join(dataDir, restorePendingFile)); err != nil {
		return "", fmt.Errorf("clear the restore marker: %w", err)
	}
	src := filepath.Join(BackupsDir(dataDir), name)
	if _, err := ValidateBackup(src, embedded); err != nil {
		return "", err
	}

	dbPath := filepath.Join(dataDir, "arrmada.db")
	pre := ""
	if _, err := os.Stat(dbPath); err == nil {
		var err error
		if pre, err = preRestoreCopy(dataDir, dbPath, log); err != nil {
			return "", fmt.Errorf("couldn't keep a copy of the current database, so nothing was changed: %w", err)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	tmp := dbPath + ".restore-tmp"
	if err := copyFileSync(src, tmp); err != nil {
		_ = os.Remove(tmp)
		return pre, fmt.Errorf("copy the backup into place: %w", err)
	}
	// The old database's write-ahead log and journal belong to it; left beside the
	// restored file SQLite would try to replay them into it. Their contents are in the
	// pre-restore copy.
	for _, side := range []string{"-wal", "-shm", "-journal"} {
		if err := os.Remove(dbPath + side); err != nil && !errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(tmp)
			return pre, fmt.Errorf("remove the old %s file: %w", side, err)
		}
	}
	if err := os.Rename(tmp, dbPath); err != nil {
		_ = os.Remove(tmp)
		return pre, fmt.Errorf("put the backup in place: %w", err)
	}
	syncDir(dataDir)
	return pre, nil
}

// preRestoreCopy keeps the database a restore is about to replace, as a pre-restore
// backup. The checked VACUUM INTO copy is the normal path; a database too damaged for
// that (often why it's being restored) is copied byte for byte instead, with its WAL.
func preRestoreCopy(dataDir, dbPath string, log *slog.Logger) (string, error) {
	dir := BackupsDir(dataDir)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	dst := freeBackupPath(dir, BackupPreRestore, time.Now())

	err := func() error {
		db, err := openDB(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		ctx := context.Background()
		if err := snapshotTo(ctx, db, dbPath, dst); err != nil {
			return err
		}
		// Fold the WAL back in, so the old database's last writes aren't only in a file
		// the swap is about to remove (they're in the copy either way).
		_, _ = db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`)
		return nil
	}()
	if err != nil {
		if errors.Is(err, ErrNoSpace) {
			return "", err
		}
		log.Warn("couldn't make a checked copy of the current database; copying its files as they are", "err", err)
		if err := checkSpace(dir, dbPath); err != nil {
			return "", err
		}
		if err := copyFileSync(dbPath, dst+".tmp"); err != nil {
			_ = os.Remove(dst + ".tmp")
			return "", err
		}
		if fi, err := os.Stat(dbPath + "-wal"); err == nil && fi.Size() > 0 {
			if err := copyFileSync(dbPath+"-wal", dst+"-wal"); err != nil {
				_ = os.Remove(dst + ".tmp")
				_ = os.Remove(dst + "-wal")
				return "", err
			}
		}
		if err := os.Rename(dst+".tmp", dst); err != nil {
			_ = os.Remove(dst + ".tmp")
			_ = os.Remove(dst + "-wal")
			return "", err
		}
		foldWAL(dst)
		syncDir(dir)
	}
	if removed, err := PruneBackups(dir, BackupPreRestore, preRestoreKeep); err != nil {
		log.Warn("couldn't prune old pre-restore copies", "err", err)
	} else if len(removed) > 0 {
		log.Info("pruned old pre-restore copies", "removed", len(removed))
	}
	return filepath.Base(dst), nil
}

// foldWAL tries to checkpoint a byte copy's WAL into the copy itself, so the copy is one
// self-contained file again (downloads and restores take only the .db). Best effort: on
// a damaged database it may not work, and the -wal then stays beside the copy.
func foldWAL(path string) {
	if _, err := os.Stat(path + "-wal"); err != nil {
		return
	}
	db, err := openDB(path)
	if err != nil {
		return
	}
	db.SetMaxOpenConns(1)
	_, _ = db.Exec(`PRAGMA wal_checkpoint(TRUNCATE)`)
	_ = db.Close() // the last close removes an empty -wal and the -shm
	_ = os.Remove(path + "-shm")
}

// copyFileSync copies src to dst (replacing it) and flushes dst to disk.
func copyFileSync(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		return err
	}
	return out.Close()
}

// writeJSONFile writes v to path through a flushed temp file and a rename, so a reader
// sees the old file or the new one, never half of one.
func writeJSONFile(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	if err := syncFile(tmp); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// readJSONFile reads path into v; ok is false when the file doesn't exist.
func readJSONFile(path string, v any) (ok bool, err error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if err := json.Unmarshal(b, v); err != nil {
		return false, err
	}
	return true, nil
}

// backupNewerError means the backup was written by a newer Arrmada than this one;
// restoring it would run this build against tables it doesn't understand. It is an
// ErrNewerSchema (schema.go), the same refusal the boot gives a newer database.
type backupNewerError struct{ unknown []string }

func (e backupNewerError) Error() string {
	return fmt.Sprintf("this backup is from a newer version of Arrmada (it has %s); update Arrmada first", strings.Join(e.unknown, ", "))
}

func (e backupNewerError) Unwrap() error { return ErrNewerSchema }
