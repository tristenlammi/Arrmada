package music

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Writes for placement (MUS-33 retags, MUS-34 renames).

// tagState is a FileRecord's state, as_found unless it says house.
func tagState(s string) string {
	if s == TagStateHouse || s == TagStateFailed {
		return s
	}
	return TagStateAsFound
}

// SetFileRetagged records that a library file was rewritten in the house style in place
// of the old one at the same path (a retag): its new size and mtime, the tag state, and
// the tags it had before, unless an earlier retag already kept the ones it arrived with.
func (r *Repo) SetFileRetagged(ctx context.Context, path string, size, mtime int64, tagsFound string) error {
	res, err := r.db.ExecContext(ctx, `
		UPDATE music_track_files SET size_bytes = ?, mtime_unix = ?, tag_state = ?, tagged_at = CURRENT_TIMESTAMP, changed_at = NULL,
		       tags_json = CASE WHEN tags_json = '' THEN ? ELSE tags_json END
		 WHERE path = ?`, size, mtime, TagStateHouse, tagsFound, path)
	return affected(res, err)
}

// ErrPathInUse is returned when a rename's new path is already another file row's.
var ErrPathInUse = errors.New("music: another file is already recorded at that path")

// MoveFilePath records that a file moved from one path to another (a rename). The row
// keeps everything else. Moving onto a path another row holds is refused; moving a path
// that has no row is a no-op (an extra the database doesn't list).
func (r *Repo) MoveFilePath(ctx context.Context, from, to string) error {
	if strings.TrimSpace(to) == "" {
		return errors.New("music: a file needs a path")
	}
	if from == to {
		return nil
	}
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		var other int64
		err := tx.QueryRowContext(ctx, `SELECT id FROM music_track_files WHERE path = ?`, to).Scan(&other)
		if err == nil {
			var old int64
			if err := tx.QueryRowContext(ctx, `SELECT id FROM music_track_files WHERE path = ?`, from).Scan(&old); errors.Is(err, sql.ErrNoRows) {
				return nil // already recorded at the new path, and nothing at the old one
			}
			return ErrPathInUse
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE music_track_files SET path = ? WHERE path = ?`, to, from)
		return err
	})
}

// SetFileRetagged records a retag (see Repo.SetFileRetagged).
func (s *Service) SetFileRetagged(ctx context.Context, path string, size, mtime int64, tagsFound string) error {
	return s.repo.SetFileRetagged(ctx, path, size, mtime, tagsFound)
}

// MoveFilePath records a file's rename (see Repo.MoveFilePath).
func (s *Service) MoveFilePath(ctx context.Context, from, to string) error {
	return s.repo.MoveFilePath(ctx, from, to)
}

// AlbumFiles lists every file row of an album.
func (s *Service) AlbumFiles(ctx context.Context, albumID int64) ([]TrackFile, error) {
	return s.repo.AlbumFiles(ctx, albumID)
}

// AlbumsWithFiles lists the ids of albums that have at least one file row, for a
// library-wide rename preview.
func (s *Service) AlbumsWithFiles(ctx context.Context) ([]int64, error) {
	rows, err := s.repo.db.QueryContext(ctx, `SELECT DISTINCT album_id FROM music_track_files WHERE missing_since IS NULL ORDER BY album_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
