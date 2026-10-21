package music

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/store"
)

// The repo side of keeping the library current (MUS-13, MUS-14): which artists are due,
// what each artist's MusicBrainz list has shown before, merges and albums MusicBrainz no
// longer lists (re-pointed or flagged, never deleted), and the artist's options.

// dbTime is how times are written here: SQLite's own CURRENT_TIMESTAMP shape, in UTC.
func dbTime(t time.Time) string { return t.UTC().Format("2006-01-02 15:04:05") }

// AlbumIDByMBID finds an album by its release-group MBID (0, false when none has it).
func (r *Repo) AlbumIDByMBID(ctx context.Context, mbid string) (int64, bool, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, `SELECT id FROM albums WHERE mbid = ?`, mbid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// ArtistIDByMBID finds an artist row by MBID, in the library or only credited.
func (r *Repo) ArtistIDByMBID(ctx context.Context, mbid string) (int64, bool, error) {
	var id int64
	err := r.db.QueryRowContext(ctx, `SELECT id FROM artists WHERE mbid = ?`, mbid).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	return id, err == nil, err
}

// SeenReleaseGroups lists every release group MusicBrainz has shown for an artist.
func (r *Repo) SeenReleaseGroups(ctx context.Context, artistID int64) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT mbid FROM music_rg_seen WHERE artist_id = ?`, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out[m] = true
	}
	return out, rows.Err()
}

// MarkSeen records release groups as shown for an artist, and the browses (scopes) that
// showed them as looked at.
func (r *Repo) MarkSeen(ctx context.Context, artistID int64, mbids []string, scopes []string) error {
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO music_rg_seen (artist_id, mbid) VALUES (?, ?) ON CONFLICT DO NOTHING`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, m := range mbids {
			if m == "" {
				continue
			}
			if _, err := stmt.ExecContext(ctx, artistID, m); err != nil {
				return err
			}
		}
		var have string
		if err := tx.QueryRowContext(ctx, `SELECT rg_seen_scopes FROM artists WHERE id = ?`, artistID).Scan(&have); err != nil {
			return err
		}
		set := SplitList(have)
		for _, s := range scopes {
			if !hasScope(have, s) {
				set = append(set, s)
			}
		}
		_, err = tx.ExecContext(ctx, `UPDATE artists SET rg_seen_scopes = ? WHERE id = ?`, strings.Join(set, ","), artistID)
		return err
	})
}

func hasScope(scopes, s string) bool {
	for _, v := range SplitList(scopes) {
		if v == s {
			return true
		}
	}
	return false
}

// StampRefreshed records when an artist's albums were last read.
func (r *Repo) StampRefreshed(ctx context.Context, artistID int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE artists SET last_refreshed_at = ? WHERE id = ?`, dbTime(at), artistID)
	return err
}

// SetArtistStale marks an artist MusicBrainz isn't answering for (kept as it is).
func (r *Repo) SetArtistStale(ctx context.Context, artistID int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE artists SET stale_since = COALESCE(stale_since, ?) WHERE id = ?`, dbTime(at), artistID)
	return err
}

// SetAlbumStale marks an album MusicBrainz no longer lists. Nothing else about it changes:
// it keeps its files, its monitoring and its editions.
func (r *Repo) SetAlbumStale(ctx context.Context, albumID int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE albums SET stale_since = COALESCE(stale_since, ?) WHERE id = ?`, dbTime(at), albumID)
	return err
}

// ErrMBIDTaken is a re-point refused because another row already has the new MBID.
var ErrMBIDTaken = errors.New("another row already has that MusicBrainz id")

// RepointAlbum moves an album to the MBID MusicBrainz merged its release group into,
// keeping the old one as a musicbrainz_old external id. Everything else stays: files,
// editions, monitoring, search history.
func (r *Repo) RepointAlbum(ctx context.Context, albumID int64, oldMBID, newMBID string) error {
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM albums WHERE mbid = ? AND id != ?`, newMBID, albumID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrMBIDTaken
		}
		res, err := tx.ExecContext(ctx, `UPDATE albums SET mbid = ?, stale_since = NULL WHERE id = ? AND mbid = ?`, newMBID, albumID, oldMBID)
		if err := affected(res, err); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO music_external_ids (entity_type, entity_id, source, value)
			VALUES ('album', ?, 'musicbrainz_old', ?) ON CONFLICT DO NOTHING`, albumID, oldMBID)
		return err
	})
}

// RepointArtist moves an artist to the MBID MusicBrainz merged it into, keeping the old
// one as a musicbrainz_old external id. Its own one-name credit follows (its key is the
// MBID), so albums found later still take the artist's credit.
func (r *Repo) RepointArtist(ctx context.Context, artistID int64, oldMBID, newMBID string) error {
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM artists WHERE mbid = ? AND id != ?`, newMBID, artistID).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			return ErrMBIDTaken
		}
		res, err := tx.ExecContext(ctx, `UPDATE artists SET mbid = ? WHERE id = ? AND mbid = ?`, newMBID, artistID, oldMBID)
		if err := affected(res, err); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE music_artist_credits SET credit_key = ?
			 WHERE credit_key = ? AND NOT EXISTS (SELECT 1 FROM music_artist_credits c2 WHERE c2.credit_key = ?)`,
			newMBID, oldMBID, newMBID); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `INSERT INTO music_external_ids (entity_type, entity_id, source, value)
			VALUES ('artist', ?, 'musicbrainz_old', ?) ON CONFLICT DO NOTHING`, artistID, oldMBID)
		return err
	})
}

// ArtistsDueForRefresh lists library artists not refreshed since before, never-refreshed
// first, then the longest ago.
func (r *Repo) ArtistsDueForRefresh(ctx context.Context, before time.Time, limit int) ([]Artist, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+artistCols+` FROM artists
		WHERE in_library = 1 AND mbid != '' AND (last_refreshed_at IS NULL OR last_refreshed_at < ?)
		ORDER BY last_refreshed_at IS NOT NULL, last_refreshed_at, id
		LIMIT ?`, dbTime(before), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Artist
	for rows.Next() {
		a, err := scanArtist(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// AlbumsWithoutListing lists monitored albums of monitored library artists that have no
// listing yet, not looked for since before: the refresh fills them so their completeness is
// known. Never looked for first.
func (r *Repo) AlbumsWithoutListing(ctx context.Context, before time.Time, limit int) ([]Album, error) {
	return r.albums(ctx, `
		JOIN artists ar ON ar.id = al.artist_id
		WHERE al.monitored = 1 AND ar.monitored = 1 AND ar.in_library = 1 AND al.mbid != ''
		  AND al.target_release_id IS NULL AND al.stale_since IS NULL
		  AND (al.listing_tried_at IS NULL OR al.listing_tried_at < ?)
		ORDER BY al.listing_tried_at IS NOT NULL, al.listing_tried_at, al.id
		LIMIT ?`, dbTime(before), limit)
}

// StampListingTried records that the refresh tried to fill an album's listing.
func (r *Repo) StampListingTried(ctx context.Context, albumID int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE albums SET listing_tried_at = ? WHERE id = ?`, dbTime(at), albumID)
	return err
}

// LegacyAlbums lists albums still on the upgrade's legacy edition whose editions haven't
// been listed since before: the background edition pass re-keys them a few at a time.
// Albums holding files go first (they're what a re-key is for), then the rest.
func (r *Repo) LegacyAlbums(ctx context.Context, before time.Time, limit int) ([]Album, error) {
	return r.albums(ctx, `
		JOIN music_releases tr ON tr.id = al.target_release_id
		WHERE tr.legacy = 1 AND al.mbid != '' AND al.source = 'musicbrainz'
		  AND (al.editions_checked_at IS NULL OR al.editions_checked_at < ?)
		ORDER BY NOT EXISTS (SELECT 1 FROM music_track_files f WHERE f.album_id = al.id),
		         al.editions_checked_at IS NOT NULL, al.editions_checked_at, al.id
		LIMIT ?`, dbTime(before), limit)
}

// PendingSingles lists singles waiting for the singles-on-an-album check that weren't
// tried since before, never-tried first.
func (r *Repo) PendingSingles(ctx context.Context, before time.Time, limit int) ([]Album, error) {
	return r.albums(ctx, ` WHERE al.found_as IN (?, ?) AND (al.listing_tried_at IS NULL OR al.listing_tried_at < ?)
		ORDER BY al.listing_tried_at IS NOT NULL, al.listing_tried_at, al.id LIMIT ?`, FoundSinglePending, FoundSingleAsked, dbTime(before), limit)
}

// SetFound records why an album is or isn't wanted, and whether it is.
func (r *Repo) SetFound(ctx context.Context, albumID int64, foundAs string, monitored bool) error {
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		if _, err := tx.ExecContext(ctx, `UPDATE albums SET found_as = ?, monitored = ? WHERE id = ?`, foundAs, b2i(monitored), albumID); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, `UPDATE music_release_tracks SET monitored = ?
			 WHERE medium_id IN (SELECT m.id FROM music_media m JOIN music_releases r ON r.id = m.release_id WHERE r.album_id = ?)`,
			b2i(monitored), albumID)
		return err
	})
}

// albumSongs is what an album's tracks are known by: recording MBIDs and folded titles,
// across every edition whose tracks are stored.
type albumSongs struct {
	recordings map[string]bool
	titles     map[string]bool
}

// songTitle is a track title as the singles check compares it: without its version
// suffix ("(Radio Edit)", "feat. …"), folded.
func songTitle(t string) string {
	base, _ := splitSuffix(t)
	if base == "" {
		base = t
	}
	return compactFold(base)
}

// songsOf reads the songs of the given albums (every stored edition).
func (r *Repo) songsOf(ctx context.Context, albumIDs []int64) (albumSongs, error) {
	out := albumSongs{recordings: map[string]bool{}, titles: map[string]bool{}}
	for _, id := range albumIDs {
		rows, err := r.db.QueryContext(ctx, `
			SELECT COALESCE(rec.mbid, ''), rt.title
			  FROM music_releases rl
			  JOIN music_media m ON m.release_id = rl.id
			  JOIN music_release_tracks rt ON rt.medium_id = m.id
			  LEFT JOIN music_recordings rec ON rec.id = rt.recording_id
			 WHERE rl.album_id = ?`, id)
		if err != nil {
			return out, err
		}
		for rows.Next() {
			var mbid, title string
			if err := rows.Scan(&mbid, &title); err != nil {
				rows.Close()
				return out, err
			}
			if mbid != "" {
				out.recordings[strings.ToLower(mbid)] = true
			}
			if t := songTitle(title); t != "" {
				out.titles[t] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return out, err
		}
	}
	return out, nil
}

// NewRelease is one album a refresh found, for the New releases list.
type NewRelease struct {
	Album
	ArtistName string `json:"artist_name"`
}

// NewReleases lists the albums refreshes found since a time, newest first.
func (r *Repo) NewReleases(ctx context.Context, since time.Time, limit int) ([]NewRelease, error) {
	albums, err := r.albums(ctx, ` WHERE al.found_at IS NOT NULL AND al.found_at >= ? ORDER BY al.found_at DESC, al.id DESC LIMIT ?`,
		dbTime(since), limit)
	if err != nil {
		return nil, err
	}
	names := map[int64]string{}
	out := make([]NewRelease, 0, len(albums))
	for _, al := range albums {
		name, ok := names[al.ArtistID]
		if !ok {
			if a, err := r.GetArtist(ctx, al.ArtistID); err == nil {
				name = a.Name
			}
			names[al.ArtistID] = name
		}
		out = append(out, NewRelease{Album: al, ArtistName: name})
	}
	return out, nil
}

// SetArtistOptions stores an artist's own new-release choice and release types ("" for
// either: follow the library default).
func (r *Repo) SetArtistOptions(ctx context.Context, artistID int64, monitorNew, releaseTypes string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE artists SET monitor_new = ?, release_types = ? WHERE id = ?`,
		monitorNew, releaseTypes, artistID)
	return affected(res, err)
}

// SetPendingAdd keeps the add dialog's choice for the first refresh that can read the
// artist's albums ("" once applied).
func (r *Repo) SetPendingAdd(ctx context.Context, artistID int64, monitor string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE artists SET pending_add_monitor = ? WHERE id = ?`, monitor, artistID)
	return err
}

// StampDepartureChecked records that an album gone from its artist's list was found still
// on MusicBrainz.
func (r *Repo) StampDepartureChecked(ctx context.Context, albumID int64, at time.Time) error {
	_, err := r.db.ExecContext(ctx, `UPDATE albums SET departure_checked_at = ? WHERE id = ?`, dbTime(at), albumID)
	return err
}

// RecentNewCount is how many new releases refreshes found for an artist since a time
// (wanted or waiting for the singles check): the rolling count the flood guard reads.
func (r *Repo) RecentNewCount(ctx context.Context, artistID int64, since time.Time) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM albums
		WHERE artist_id = ? AND found_at >= ? AND found_as IN (?, ?)`,
		artistID, dbTime(since), FoundNew, FoundSinglePending).Scan(&n)
	return n, err
}

// AlbumsWithAnyFile lists the artist's albums holding any file row at all, missing ones
// included: a library share that isn't mounted must never make an album look empty.
func (r *Repo) AlbumsWithAnyFile(ctx context.Context, artistID int64) (map[int64]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT DISTINCT f.album_id FROM music_track_files f
		JOIN albums al ON al.id = f.album_id WHERE al.artist_id = ?`, artistID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]bool{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// ClearPendingSingle settles a single a person wanted or unwanted by hand: their choice
// stands, and the automatic singles check leaves it alone.
func (r *Repo) ClearPendingSingle(ctx context.Context, albumID int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE albums SET found_as = '' WHERE id = ? AND found_as IN (?, ?)`,
		albumID, FoundSinglePending, FoundSingleAsked)
	return err
}

// applyPlan carries out an apply plan in one transaction: want, unwant (album and
// tracks, as SetAlbumMonitored does) and put singles through the asked-for check.
func (r *Repo) applyPlan(ctx context.Context, want, unwant, singles []int64) error {
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		set := func(ids []int64, monitored bool) error {
			for _, id := range ids {
				if _, err := tx.ExecContext(ctx, `UPDATE albums SET monitored = ? WHERE id = ?`, b2i(monitored), id); err != nil {
					return err
				}
				if _, err := tx.ExecContext(ctx, `UPDATE music_release_tracks SET monitored = ?
					 WHERE medium_id IN (SELECT m.id FROM music_media m JOIN music_releases rl ON rl.id = m.release_id WHERE rl.album_id = ?)`,
					b2i(monitored), id); err != nil {
					return err
				}
			}
			return nil
		}
		if err := set(want, true); err != nil {
			return err
		}
		if err := set(unwant, false); err != nil {
			return err
		}
		for _, id := range singles {
			if _, err := tx.ExecContext(ctx, `UPDATE albums SET found_as = ?, listing_tried_at = NULL WHERE id = ?`, FoundSingleAsked, id); err != nil {
				return err
			}
		}
		return nil
	})
}
