package store

import (
	"context"
	"fmt"
	"time"
)

// SafetyCopy writes a consistent copy of the live database to
// <dataDir>/backups/arrmada-<kind>-<UTC timestamp>.db before something rewrites data that
// can't be put back (deleting a user cascades to their audiobook places and listening
// history), then keeps only the newest `keep` copies of that kind. Other files in the
// backups folder are never touched.
//
// It is Snapshot plus pruning: the same checked VACUUM INTO copy the pre-migration backup
// uses, so every copy in the folder is made one way. kind must be one of the BackupKind
// names, which keeps it from steering the file name anywhere else. dataDir is kept for
// callers' sake; the copy always goes next to the database this Store opened.
func (s *Store) SafetyCopy(ctx context.Context, dataDir, kind string, keep int) (string, error) {
	_ = dataDir
	k := BackupKind(kind)
	if _, _, ok := ParseBackupName(BackupName(k, time.Now())); !ok {
		return "", fmt.Errorf("invalid backup kind %q", kind)
	}
	path, err := s.Snapshot(ctx, k)
	if err != nil {
		return "", err
	}
	if keep > 0 {
		_, _ = PruneBackups(BackupsDir(s.dataDir), k, keep)
	}
	return path, nil
}
