package music

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/tristenlammi/arrmada/internal/store"
)

// Reads the matcher needs beyond an edition's tracks (MUS-31).

// albumsWithRecordings lists the albums whose stored editions carry any of the recording
// MBIDs.
func (r *Repo) albumsWithRecordings(ctx context.Context, mbids []string) ([]int64, error) {
	args := make([]any, 0, len(mbids))
	seen := map[string]bool{}
	for _, m := range mbids {
		if m = strings.ToLower(strings.TrimSpace(m)); m != "" && !seen[m] {
			seen[m] = true
			args = append(args, m)
		}
		if len(args) == 500 {
			break // SQLite's variable limit is far above this; a match never has more files
		}
	}
	if len(args) == 0 {
		return nil, nil
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT DISTINCT r.album_id FROM music_recordings rec
		  JOIN music_release_tracks rt ON rt.recording_id = rec.id
		  JOIN music_media m ON m.id = rt.medium_id
		  JOIN music_releases r ON r.id = m.release_id
		 WHERE lower(rec.mbid) IN (?`+strings.Repeat(", ?", len(args)-1)+`)
		 ORDER BY r.album_id`, args...)
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

// albumByMBID is the library album with a release-group MBID (0: none).
func (r *Repo) albumByMBID(ctx context.Context, mbid string) (int64, error) {
	mbid = strings.TrimSpace(mbid)
	if mbid == "" {
		return 0, nil
	}
	var id int64
	err := r.db.QueryRowContext(ctx, `SELECT id FROM albums WHERE `+store.FoldASCII(r.db, "mbid")+` = `+store.FoldASCII(r.db, "?"), mbid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// albumOfRelease is the album a stored edition MBID belongs to (0: not stored).
func (r *Repo) albumOfRelease(ctx context.Context, mbid string) (int64, error) {
	mbid = strings.TrimSpace(mbid)
	if mbid == "" {
		return 0, nil
	}
	var id int64
	err := r.db.QueryRowContext(ctx, `SELECT album_id FROM music_releases WHERE `+store.FoldASCII(r.db, "mbid")+` = `+store.FoldASCII(r.db, "?"), mbid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

// recordingIDs reads the ISRCs and merged-away MBIDs stored for an edition's recordings,
// in one query.
func (r *Repo) recordingIDs(ctx context.Context, releaseID int64) (isrcs, old map[int64][]string, err error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT x.entity_id, x.source, x.value FROM music_external_ids x
		 WHERE x.entity_type = 'recording' AND x.source IN ('isrc', 'musicbrainz_old')
		   AND x.entity_id IN (SELECT rt.recording_id FROM music_release_tracks rt
		                         JOIN music_media m ON m.id = rt.medium_id WHERE m.release_id = ?)`, releaseID)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	isrcs, old = map[int64][]string{}, map[int64][]string{}
	for rows.Next() {
		var id int64
		var src, val string
		if err := rows.Scan(&id, &src, &val); err != nil {
			return nil, nil, err
		}
		if src == "isrc" {
			isrcs[id] = append(isrcs[id], val)
		} else {
			old[id] = append(old[id], val)
		}
	}
	return isrcs, old, rows.Err()
}
