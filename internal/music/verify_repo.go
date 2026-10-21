package music

import (
	"context"
	"time"

	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The verify pass's reads and writes (MUS-15). One read of every recorded file, and one
// transaction for everything a pass decides. Nothing here deletes a row or forgets a file:
// a file gone from disk is marked, with the row and its track kept, so a remount links it
// straight back.

// VerifyRow is one recorded file as the verify pass sees it.
type VerifyRow struct {
	ID, AlbumID, ArtistID, TrackID int64
	AlbumTitle, Path               string
	SizeBytes, MtimeUnix           int64
	FileID                         string
	MissingSince, NotFoundSince    string
	TagState                       string
}

// VerifyRows lists every recorded file with its album and artist, in one statement.
func (s *Service) VerifyRows(ctx context.Context) ([]VerifyRow, error) {
	rows, err := s.repo.db.QueryContext(ctx, `
		SELECT f.id, f.album_id, al.artist_id, COALESCE(f.release_track_id, 0), al.title, f.path,
		       f.size_bytes, f.mtime_unix, f.file_id, COALESCE(f.missing_since, ''), COALESCE(f.missing_seen_at, ''),
		       f.tag_state
		  FROM music_track_files f JOIN albums al ON al.id = f.album_id
		 WHERE f.path != '' ORDER BY f.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VerifyRow
	for rows.Next() {
		var v VerifyRow
		if err := rows.Scan(&v.ID, &v.AlbumID, &v.ArtistID, &v.TrackID, &v.AlbumTitle, &v.Path, &v.SizeBytes, &v.MtimeUnix,
			&v.FileID, &v.MissingSince, &v.NotFoundSince, &v.TagState); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// VerifyBaseline is a file's size, time and inode as seen now.
type VerifyBaseline struct {
	ID          int64
	Path        string
	Size, Mtime int64
	FileID      string
}

// VerifyChange is a file found changed outside Arrmada: what it is now, what it measured
// (nil: the probe couldn't run, so the measuring pass tries later) and its tag state.
type VerifyChange struct {
	VerifyBaseline
	Probe    *mediainfo.AudioInfo
	Tier     Quality
	TagState string
}

// VerifyWrites is everything one verify pass decided.
type VerifyWrites struct {
	At       time.Time
	Back     []VerifyBaseline // seen again: no longer missing or not found
	NotFound []VerifyBaseline // not found for the first time: the grace period starts
	Missing  []VerifyBaseline // still not found after the grace period: missing
	Baseline []VerifyBaseline // the same file, with a size, time or inode not yet recorded
	Changed  []VerifyChange
	// Redevice are files not on disk whose recorded device was renumbered (a reboot or
	// remount): only FileID is written.
	Redevice []VerifyBaseline
}

// VerifyApplied counts the rows each kind of write changed.
type VerifyApplied struct {
	Back, NotFound, Missing, Changed int
}

// ApplyVerify writes a verify pass's decisions in one transaction. Every write is guarded
// on the row's path (a file replaced or renamed since the pass read it is left alone) and
// on its state, so a second pass deciding the same thing changes nothing.
func (s *Service) ApplyVerify(ctx context.Context, w VerifyWrites) (VerifyApplied, error) {
	var out VerifyApplied
	at := w.At.UTC().Format(time.RFC3339)
	err := store.WithTx(ctx, s.repo.db, func(tx *store.Transaction) error {
		out = VerifyApplied{}
		exec := func(q string, args ...any) (int, error) {
			res, err := tx.ExecContext(ctx, q, args...)
			if err != nil {
				return 0, err
			}
			n, _ := res.RowsAffected()
			return int(n), nil
		}
		for _, b := range w.Back {
			n, err := exec(`UPDATE music_track_files SET missing_since = NULL, missing_seen_at = NULL
				WHERE id = ? AND path = ? AND (missing_since IS NOT NULL OR missing_seen_at IS NOT NULL)`, b.ID, b.Path)
			if err != nil {
				return err
			}
			out.Back += n
		}
		for _, b := range w.NotFound {
			n, err := exec(`UPDATE music_track_files SET missing_seen_at = ?
				WHERE id = ? AND path = ? AND missing_since IS NULL AND missing_seen_at IS NULL`, at, b.ID, b.Path)
			if err != nil {
				return err
			}
			out.NotFound += n
		}
		for _, b := range w.Missing {
			n, err := exec(`UPDATE music_track_files SET missing_since = missing_seen_at
				WHERE id = ? AND path = ? AND missing_since IS NULL AND missing_seen_at IS NOT NULL`, b.ID, b.Path)
			if err != nil {
				return err
			}
			out.Missing += n
		}
		for _, b := range w.Redevice {
			if _, err := exec(`UPDATE music_track_files SET file_id = ? WHERE id = ? AND path = ?`, b.FileID, b.ID, b.Path); err != nil {
				return err
			}
		}
		for _, b := range w.Baseline {
			if _, err := exec(`UPDATE music_track_files SET size_bytes = ?, mtime_unix = ?, file_id = ? WHERE id = ? AND path = ?`,
				b.Size, b.Mtime, b.FileID, b.ID, b.Path); err != nil {
				return err
			}
		}
		for _, c := range w.Changed {
			var n int
			var err error
			if c.Probe != nil {
				p := c.Probe
				n, err = exec(`UPDATE music_track_files SET size_bytes = ?, mtime_unix = ?, file_id = ?, changed_at = ?,
					  tag_state = ?, quality = ?, bitrate_kbps = ?, lossless = ?, codec = ?, sample_rate_hz = ?, bit_depth = ?,
					  channels = ?, duration_ms = ?, probed_at = CURRENT_TIMESTAMP
					WHERE id = ? AND path = ?`,
					c.Size, c.Mtime, c.FileID, at, tagState(c.TagState), string(c.Tier), p.BitrateKbps, b2i(IsLosslessQuality(c.Tier)),
					p.Codec, p.SampleRateHz, p.BitsPerSample, p.Channels, int(p.DurationSec*1000), c.ID, c.Path)
			} else {
				// Not measured now: the measuring pass picks it up (probed_at NULL).
				n, err = exec(`UPDATE music_track_files SET size_bytes = ?, mtime_unix = ?, file_id = ?, changed_at = ?,
					  tag_state = ?, probed_at = NULL
					WHERE id = ? AND path = ?`,
					c.Size, c.Mtime, c.FileID, at, tagState(c.TagState), c.ID, c.Path)
			}
			if err != nil {
				return err
			}
			out.Changed += n
		}
		return nil
	})
	return out, err
}

// AlbumsWithMissingFiles lists the albums with a track file gone from disk (see
// Repo.AlbumsWithMissingFiles).
func (s *Service) AlbumsWithMissingFiles(ctx context.Context) ([]Album, error) {
	return s.repo.AlbumsWithMissingFiles(ctx)
}
