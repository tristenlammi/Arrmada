package store

import (
	"path/filepath"
	"regexp"
	"time"
)

// BackupKind says why a database copy was taken. It is part of the file name, so
// pruning one kind can never touch another.
type BackupKind string

const (
	BackupPreMigrate    BackupKind = "pre-migrate"
	BackupNightly       BackupKind = "nightly"
	BackupManual        BackupKind = "manual"
	BackupPreRestore    BackupKind = "pre-restore"
	BackupPreDeleteUser BackupKind = "pre-delete-user"
	// BackupPreDeleteEmptyUser is the copy before deleting an account with nothing to lose,
	// kept apart so clearing out guest accounts can't prune the copy holding someone's
	// audiobook places.
	BackupPreDeleteEmptyUser BackupKind = "pre-delete-empty-user"
	BackupUploaded           BackupKind = "uploaded"
)

// backupStamp is the UTC timestamp in a backup's file name: sortable, and free of
// characters that are awkward in a path.
const backupStamp = "20060102T150405Z"

// backupNameRe is deliberately strict: anything that isn't exactly one of our
// names (a path, a "..", someone's own file in the folder) is not a backup.
var backupNameRe = regexp.MustCompile(`^arrmada-(pre-migrate|nightly|manual|pre-restore|pre-delete-user|pre-delete-empty-user|uploaded)-(\d{8}T\d{6}Z)\.db$`)

// BackupName is the file name for a backup of kind taken at t.
func BackupName(kind BackupKind, t time.Time) string {
	return "arrmada-" + string(kind) + "-" + t.UTC().Format(backupStamp) + ".db"
}

// ParseBackupName reverses BackupName. ok is false for any name it didn't make.
func ParseBackupName(name string) (kind BackupKind, at time.Time, ok bool) {
	m := backupNameRe.FindStringSubmatch(name)
	if m == nil {
		return "", time.Time{}, false
	}
	at, err := time.Parse(backupStamp, m[2])
	if err != nil {
		return "", time.Time{}, false
	}
	return BackupKind(m[1]), at, true
}

// BackupsDir is where database copies live, next to the database itself.
func BackupsDir(dataDir string) string { return filepath.Join(dataDir, "backups") }

// BackupsDir is where this store's Snapshot writes, next to the database it opened.
func (s *Store) BackupsDir() string { return BackupsDir(s.dataDir) }
