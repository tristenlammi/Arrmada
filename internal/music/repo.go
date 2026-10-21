package music

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Repo persists the music library in SQLite: artists and albums (release groups), and
// under each album its editions, discs, tracks and files (the model of migration 0339,
// MUS-28). The old tracks table is frozen: nothing here reads or writes it (archtest).
type Repo struct{ db store.Pool }

// NewRepo builds a repository over the given pool.
func NewRepo(db store.Pool) *Repo { return &Repo{db: db} }

const artistCols = `id, mbid, name, sort_name, overview, image_url, genres_json,
	monitored, quality_profile, added_at, disambiguation, artist_type, country, in_library,
	last_refreshed_at, stale_since, monitor_new, release_types, rg_seen_scopes, pending_add_monitor`

func scanArtist(row interface{ Scan(...any) error }) (Artist, error) {
	var (
		a                 Artist
		genresJSON, types string
		mon, inLib        int
		refreshed, stale  sql.NullString
	)
	if err := row.Scan(&a.ID, &a.MBID, &a.Name, &a.SortName, &a.Overview, &a.ImageURL,
		&genresJSON, &mon, &a.QualityProfile, &a.AddedAt, &a.Disambiguation, &a.Type, &a.Country, &inLib,
		&refreshed, &stale, &a.MonitorNew, &types, &a.seenScopes, &a.pendingAdd); err != nil {
		return Artist{}, err
	}
	a.Monitored, a.InLibrary = mon != 0, inLib != 0
	a.LastRefreshedAt, a.StaleSince = refreshed.String, stale.String
	if genresJSON != "" {
		_ = json.Unmarshal([]byte(genresJSON), &a.Genres)
	}
	if types != "" {
		// Stored values were checked when saved; one that no longer reads (a type
		// MusicBrainz retired) falls back to the library default rather than hiding the
		// artist's albums.
		if rt, err := ParseReleaseTypes(types); err == nil {
			a.ReleaseTypes = &rt
		}
	}
	return a, nil
}

// ListArtists returns every artist in the library with its holdings summary, newest
// first. Artists that are only credited (featured on someone else's album) aren't listed.
func (r *Repo) ListArtists(ctx context.Context) ([]Artist, error) {
	out, err := r.LibraryArtists(ctx)
	if err != nil {
		return nil, err
	}
	stats, err := r.artistStats(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if s, ok := stats[out[i].ID]; ok {
			out[i].Stats = s
		} else {
			out[i].Stats = &ArtistStats{}
		}
	}
	return out, nil
}

// LibraryArtists returns the library's artists, newest first, without their stats: one
// cheap read for callers that only need names, profiles and monitoring (the sweep,
// Wanted, matching a download to an album), where the stats would cost a pass over every
// album's tracks.
func (r *Repo) LibraryArtists(ctx context.Context) ([]Artist, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+artistCols+` FROM artists WHERE in_library = 1 ORDER BY added_at DESC, id DESC`)
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

// wantedTrack says whether a release track ("rt") of an album ("al") counts towards the
// album's tracks. Album and artist switches cascade to their tracks (as they always have),
// so a track's own flag only means something inside a monitored album: an unmonitored
// album still counts all its tracks, and its page still says 12/12.
const wantedTrack = `(rt.monitored = 1 OR al.monitored = 0)`

// albumCounts are an album row's ("al") counts, as correlated subqueries on its target
// edition: wanted tracks, those with a file on disk, the size of every file the album
// holds (extras included), and every track of the edition (0: no listing yet). Indexed
// lookups all, so a list of thousands of albums is still one quick statement.
const albumCounts = `
	(SELECT COUNT(*) FROM music_media m JOIN music_release_tracks rt ON rt.medium_id = m.id
	  WHERE m.release_id = al.target_release_id AND ` + wantedTrack + `),
	(SELECT COUNT(*) FROM music_media m JOIN music_release_tracks rt ON rt.medium_id = m.id
	   JOIN music_track_files f ON f.release_track_id = rt.id
	  WHERE m.release_id = al.target_release_id AND f.missing_since IS NULL AND ` + wantedTrack + `),
	(SELECT COALESCE(SUM(f.size_bytes), 0) FROM music_track_files f WHERE f.album_id = al.id AND f.missing_since IS NULL),
	(SELECT COUNT(*) FROM music_media m JOIN music_release_tracks rt ON rt.medium_id = m.id
	  WHERE m.release_id = al.target_release_id)`

// artistStats summarizes every library artist's albums in one statement, with the same
// counts and arithmetic as statsFromAlbums (which the detail read uses), so list and
// detail agree. About a third of a second on a 15,000-album, 200,000-track library.
func (r *Repo) artistStats(ctx context.Context) (map[int64]*ArtistStats, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT al.artist_id, CASE WHEN al.monitored = 1 AND al.stale_since IS NULL THEN 1 ELSE 0 END,`+albumCounts+` FROM albums al`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]*ArtistStats{}
	for rows.Next() {
		var (
			id                              int64
			mon, tracks, have, editionCount int
			size                            int64
		)
		if err := rows.Scan(&id, &mon, &tracks, &have, &size, &editionCount); err != nil {
			return nil, err
		}
		s := out[id]
		if s == nil {
			s = &ArtistStats{}
			out[id] = s
		}
		s.add(tracks, have, editionCount, size, mon != 0)
	}
	return out, rows.Err()
}

// HasArtists reports whether the library holds any artist at all. A cheap EXISTS rather
// than a count: it's asked at startup only to tell "never used Music" from "uses it".
func (r *Repo) HasArtists(ctx context.Context) (bool, error) {
	var has bool
	err := r.db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM artists WHERE in_library = 1)`).Scan(&has)
	return has, err
}

// GetArtist returns one artist by id (without albums).
func (r *Repo) GetArtist(ctx context.Context, id int64) (Artist, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+artistCols+` FROM artists WHERE id = ?`, id)
	a, err := scanArtist(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Artist{}, ErrNotFound
	}
	return a, err
}

// CreateArtist adds an artist to the library, with its own one-name credit. An artist
// already stored only as a credit (featured somewhere, or removed from the library while
// still credited) is brought back into the library instead; one already in it is
// ErrExists.
func (r *Repo) CreateArtist(ctx context.Context, a Artist) (Artist, error) {
	genresJSON := ""
	if len(a.Genres) > 0 {
		if raw, err := json.Marshal(a.Genres); err == nil {
			genresJSON = string(raw)
		}
	}
	var id int64
	err := store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		res, err := tx.ExecContext(ctx, `
			INSERT INTO artists (mbid, name, sort_name, overview, image_url, genres_json, monitored, quality_profile,
			                     disambiguation, artist_type, country, in_library)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 1)
			ON CONFLICT(mbid) DO UPDATE SET
			  name = excluded.name, sort_name = excluded.sort_name, overview = excluded.overview,
			  genres_json = excluded.genres_json, monitored = excluded.monitored,
			  quality_profile = excluded.quality_profile, disambiguation = excluded.disambiguation,
			  artist_type = excluded.artist_type, country = excluded.country, in_library = 1
			WHERE artists.in_library = 0`,
			a.MBID, a.Name, a.SortName, a.Overview, a.ImageURL, genresJSON, b2i(a.Monitored), a.QualityProfile,
			a.Disambiguation, a.Type, a.Country)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrExists // already in the library: the conflict's WHERE kept it as it was
		}
		if err := tx.QueryRowContext(ctx, `SELECT id FROM artists WHERE mbid = ?`, a.MBID).Scan(&id); err != nil {
			return err
		}
		_, err = artistCreditTx(ctx, tx, id)
		return err
	})
	if err != nil {
		return Artist{}, err
	}
	return r.GetArtist(ctx, id)
}

// UpdateArtistMeta refreshes provider-owned artist fields, leaving the user's choices alone.
func (r *Repo) UpdateArtistMeta(ctx context.Context, id int64, overview, imageURL string, genres []string, disambiguation, typ, country string) error {
	genresJSON := ""
	if len(genres) > 0 {
		if raw, err := json.Marshal(genres); err == nil {
			genresJSON = string(raw)
		}
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE artists SET overview = ?, image_url = ?, genres_json = ?, disambiguation = ?, artist_type = ?,
		        country = ?, stale_since = NULL
		  WHERE id = ?`,
		overview, imageURL, genresJSON, disambiguation, typ, country, id)
	return err
}

// albumTracksOf switches the release tracks of every edition of the albums the given
// subquery names, for the monitoring cascades.
const albumTracksOf = `UPDATE music_release_tracks SET monitored = ?
	 WHERE medium_id IN (SELECT m.id FROM music_media m JOIN music_releases r ON r.id = m.release_id
	                      WHERE r.album_id IN (%s))`

// SetArtistMonitored toggles an artist and cascades to its albums and their tracks, so an
// unmonitored artist can't leave monitored albums behind that the searcher keeps chasing.
func (r *Repo) SetArtistMonitored(ctx context.Context, id int64, monitored bool) error {
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		if _, err := tx.ExecContext(ctx, `UPDATE artists SET monitored = ? WHERE id = ?`, b2i(monitored), id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE albums SET monitored = ? WHERE artist_id = ?`, b2i(monitored), id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, fmt.Sprintf(albumTracksOf, `SELECT id FROM albums WHERE artist_id = ?`), b2i(monitored), id)
		return err
	})
}

// SetArtistQualityProfile changes an artist's profile.
func (r *Repo) SetArtistQualityProfile(ctx context.Context, id int64, profile string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE artists SET quality_profile = ? WHERE id = ?`, profile, id)
	return affected(res, err)
}

// albumCredits lists the credits an album ("al") carries: its own, its editions', and
// its editions' tracks'.
const albumCredits = `
	SELECT al.artist_credit_id
	UNION SELECT r.artist_credit_id FROM music_releases r WHERE r.album_id = al.id
	UNION SELECT rt.artist_credit_id FROM music_releases r
	        JOIN music_media m ON m.release_id = r.id
	        JOIN music_release_tracks rt ON rt.medium_id = m.id
	       WHERE r.album_id = al.id AND rt.artist_credit_id IS NOT NULL`

// DeleteArtist removes an artist from the library. Rows only: no file is touched.
//
// Credits may still name it (featured on another library artist's album), so it isn't
// simply deleted. It leaves the library (in_library = 0, unmonitored), and the albums
// filed under it go, except any that another library artist is credited on: those stay,
// credit and all. An artist that nothing credits any more is then removed outright, its
// history with it, which is what deleting an artist always did.
func (r *Repo) DeleteArtist(ctx context.Context, id int64) error {
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		res, err := tx.ExecContext(ctx, `UPDATE artists SET in_library = 0, monitored = 0 WHERE id = ?`, id)
		if err := affected(res, err); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM albums WHERE id IN (
			  SELECT al.id FROM albums al
			   WHERE al.artist_id = ?1
			     AND NOT EXISTS (SELECT 1 FROM music_artist_credit_names n JOIN artists o ON o.id = n.artist_id
			                      WHERE o.id != ?1 AND o.in_library = 1 AND n.credit_id IN (`+albumCredits+`)))`, id); err != nil {
			return err
		}
		// Recordings that were only on the albums just deleted, and credits nothing uses
		// any more (the artist's own one-name credit among them).
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM music_recordings
			 WHERE NOT EXISTS (SELECT 1 FROM music_release_tracks rt WHERE rt.recording_id = music_recordings.id)`); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `
			DELETE FROM music_artist_credits
			 WHERE id IN (SELECT credit_id FROM music_artist_credit_names WHERE artist_id = ?)
			   AND NOT EXISTS (SELECT 1 FROM albums WHERE artist_credit_id = music_artist_credits.id)
			   AND NOT EXISTS (SELECT 1 FROM music_releases WHERE artist_credit_id = music_artist_credits.id)
			   AND NOT EXISTS (SELECT 1 FROM music_recordings WHERE artist_credit_id = music_artist_credits.id)
			   AND NOT EXISTS (SELECT 1 FROM music_release_tracks WHERE artist_credit_id = music_artist_credits.id)`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `
			DELETE FROM artists
			 WHERE id = ?1
			   AND NOT EXISTS (SELECT 1 FROM music_artist_credit_names WHERE artist_id = ?1)
			   AND NOT EXISTS (SELECT 1 FROM albums WHERE artist_id = ?1)`, id)
		return err
	})
}

// albumHead is an album row's ("al") own columns, its credit and its edition, in
// scanAlbum's order; the counts follow.
const albumHead = `SELECT al.id, al.artist_id, al.mbid, al.title, al.year, al.album_type, al.cover_url,
	al.release_date, al.monitored, al.added_at, al.last_search_at, al.search_misses, al.primary_type,
	al.secondary_types, COALESCE(al.artist_credit_id, 0),
	COALESCE((SELECT c.display FROM music_artist_credits c WHERE c.id = al.artist_credit_id), ''),
	COALESCE(al.target_release_id, 0), al.edition_pinned, al.found_at, al.found_as, al.stale_since, al.source, al.departure_checked_at,`

// albumMarks are an album row's verify-pass markers (MUS-15): its track files gone from
// disk, and those changed outside Arrmada. Indexed by album, like the counts.
const albumMarks = `,
	(SELECT COUNT(*) FROM music_track_files f WHERE f.album_id = al.id AND f.release_track_id IS NOT NULL AND f.missing_since IS NOT NULL),
	(SELECT COUNT(*) FROM music_track_files f WHERE f.album_id = al.id AND f.changed_at IS NOT NULL AND f.missing_since IS NULL)`

// albumSelect reads album rows with their counts.
const albumSelect = albumHead + albumCounts + albumMarks + ` FROM albums al`

func scanAlbum(row interface{ Scan(...any) error }) (Album, error) {
	var (
		al                           Album
		mon, pinned                  int
		last, found, stale, departed sql.NullString
	)
	if err := row.Scan(&al.ID, &al.ArtistID, &al.MBID, &al.Title, &al.Year, &al.AlbumType, &al.CoverURL,
		&al.ReleaseDate, &mon, &al.AddedAt, &last, &al.SearchMisses, &al.PrimaryType, &al.SecondaryTypes,
		&al.CreditID, &al.ArtistCredit, &al.TargetEditionID, &pinned, &found, &al.FoundAs, &stale, &al.Source, &departed,
		&al.TrackCount, &al.HaveTracks, &al.SizeBytes, &al.EditionTracks, &al.MissingTracks, &al.ChangedTracks); err != nil {
		return Album{}, err
	}
	al.Monitored, al.EditionPinned = mon != 0, pinned != 0
	al.LastSearchAt, al.FoundAt, al.StaleSince, al.departureChecked = last.String, found.String, stale.String, departed.String
	return al, nil
}

func (r *Repo) albums(ctx context.Context, q string, args ...any) ([]Album, error) {
	rows, err := r.db.QueryContext(ctx, albumSelect+q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Album
	for rows.Next() {
		al, err := scanAlbum(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, al)
	}
	return out, rows.Err()
}

// AlbumsFor returns the albums filed under an artist, newest first, each with its counts.
func (r *Repo) AlbumsFor(ctx context.Context, artistID int64) ([]Album, error) {
	return r.albums(ctx, ` WHERE al.artist_id = ? ORDER BY al.year DESC, al.title`, artistID)
}

// AppearsOn returns the albums filed under other artists that credit this one: on the
// album, an edition, or a track of one.
func (r *Repo) AppearsOn(ctx context.Context, artistID int64) ([]Album, error) {
	return r.albums(ctx, `
		WHERE al.artist_id != ?1 AND al.id IN (
		      SELECT a2.id FROM albums a2 JOIN music_artist_credit_names n ON n.credit_id = a2.artist_credit_id
		       WHERE n.artist_id = ?1
		      UNION SELECT r.album_id FROM music_releases r JOIN music_artist_credit_names n ON n.credit_id = r.artist_credit_id
		       WHERE n.artist_id = ?1
		      UNION SELECT r.album_id FROM music_releases r
		        JOIN music_media m ON m.release_id = r.id
		        JOIN music_release_tracks rt ON rt.medium_id = m.id
		        JOIN music_artist_credit_names n ON n.credit_id = rt.artist_credit_id
		       WHERE n.artist_id = ?1)
		ORDER BY al.year DESC, al.title`, artistID)
}

// AlbumHit is one row of an album search: just enough to label it in a picker.
type AlbumHit struct {
	ID       int64
	Title    string
	Year     int
	Artist   string
	CoverURL string
}

// SearchAlbums finds library albums whose title or artist contains q (case-insensitive;
// SQLite's LIKE folds ASCII case). An empty q lists everything up to limit. It runs in
// SQL rather than in memory because a big discography makes loading every album with
// its track counts just to filter a picker far too slow.
func (r *Repo) SearchAlbums(ctx context.Context, q string, limit int) ([]AlbumHit, error) {
	pat := "%" + escapeLike(q) + "%"
	rows, err := r.db.QueryContext(ctx,
		`SELECT al.id, al.title, al.year, ar.name, al.cover_url
		   FROM albums al JOIN artists ar ON ar.id = al.artist_id
		  WHERE `+store.FoldASCII(r.db, "al.title")+` LIKE `+store.FoldASCII(r.db, "?")+` ESCAPE '\' OR `+store.FoldASCII(r.db, "ar.name")+` LIKE `+store.FoldASCII(r.db, "?")+` ESCAPE '\'
		  ORDER BY `+store.FoldASCII(r.db, "ar.name")+`, al.year, `+store.FoldASCII(r.db, "al.title")+`
		  LIMIT ?`, pat, pat, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []AlbumHit
	for rows.Next() {
		var h AlbumHit
		if err := rows.Scan(&h.ID, &h.Title, &h.Year, &h.Artist, &h.CoverURL); err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, rows.Err()
}

// escapeLike makes q match literally inside a LIKE pattern, so a search for "100%" or
// "a_b" doesn't turn into a wildcard.
func escapeLike(q string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
}

// GetAlbum returns one album by id with its counts (without tracks).
func (r *Repo) GetAlbum(ctx context.Context, id int64) (Album, error) {
	al, err := scanAlbum(r.db.QueryRowContext(ctx, albumSelect+` WHERE al.id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Album{}, ErrNotFound
	}
	return al, err
}

// UpsertAlbum inserts an album, or updates the provider-owned fields when its MBID is
// already present — so a metadata refresh corrects a title or cover without creating a
// duplicate row or disturbing the user's monitoring choice. A new album takes its
// artist's own credit.
func (r *Repo) UpsertAlbum(ctx context.Context, al Album) (int64, error) {
	primary, secondary := al.PrimaryType, al.SecondaryTypes
	if primary == "" && secondary == "" {
		primary, secondary = splitAlbumType(al.AlbumType)
	}
	var foundAt sql.NullString
	if al.FoundAt != "" {
		foundAt = sql.NullString{String: al.FoundAt, Valid: true}
	}
	var id int64
	err := r.db.QueryRowContext(ctx,
		`INSERT INTO albums (artist_id, mbid, title, year, album_type, cover_url, release_date, monitored,
		                     primary_type, secondary_types, found_at, found_as, artist_credit_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?,
		         (SELECT c.id FROM artists a JOIN music_artist_credits c ON c.credit_key = a.mbid WHERE a.id = ?))
		 ON CONFLICT(mbid) DO UPDATE SET
		   title = excluded.title, year = excluded.year, album_type = excluded.album_type,
		   cover_url = excluded.cover_url, release_date = excluded.release_date,
		   primary_type = excluded.primary_type, secondary_types = excluded.secondary_types,
		   artist_credit_id = COALESCE(albums.artist_credit_id, excluded.artist_credit_id),
		   stale_since = NULL RETURNING id`,
		al.ArtistID, al.MBID, al.Title, al.Year, al.AlbumType, al.CoverURL, al.ReleaseDate, b2i(al.Monitored),
		primary, secondary, foundAt, al.FoundAs, al.ArtistID).Scan(&id)
	return id, err
}

// WantedAlbums returns every monitored album of a monitored artist, each with its track and
// have counts, in one statement: the sweep's and Wanted's list.
//
// An album with an open migration note (openNoteOf) is left out. The old table said
// those tracks had a file, the upgrade couldn't give them one, and searching would grab
// the whole album again over what is most likely a scan's mix-up. A person resolves the
// note on the album page, or the track gets its file, and the album comes back.
//
// An album MusicBrainz no longer lists (stale_since) is left out too: it is kept with its
// files, and a person can still search for it by hand, but nothing searches it by itself.
//
// So is an album with a track file the file check found gone from disk (MUS-15): the file
// may only be renamed, or on a disk that's away, and downloading it again by itself could
// fill the library with copies of music the owner still has. The album page says the
// files are gone and offers "Search for missing tracks"; that, or the file coming back,
// is the way out. As movies do (movies/missing.go): flag, never re-want.
func (r *Repo) WantedAlbums(ctx context.Context) ([]Album, error) {
	return r.albums(ctx, `
		JOIN artists ar ON ar.id = al.artist_id
		WHERE al.monitored = 1 AND ar.monitored = 1 AND al.stale_since IS NULL
		  AND NOT EXISTS (SELECT 1 FROM music_migration_notes n WHERE n.album_id = al.id AND `+openNote+`)
		  AND NOT EXISTS (SELECT 1 FROM music_track_files mf WHERE mf.album_id = al.id AND `+missingFile+`)
		ORDER BY al.id`)
}

// missingFile says whether a track file row ("mf") is one the file check found gone from
// disk: the one rule WantedAlbums, IncompleteWanted (the Dashboard) and the "Search for
// missing tracks" list share.
const missingFile = `mf.missing_since IS NOT NULL AND mf.release_track_id IS NOT NULL`

// AlbumsWithMissingFiles lists the albums with a track file the file check found gone
// from disk, for the "Search for missing tracks" bulk action: never searched first, then
// the longest since their last search, so runs capped at a few albums each reach them all.
// Monitoring doesn't matter: the person asked.
func (r *Repo) AlbumsWithMissingFiles(ctx context.Context) ([]Album, error) {
	return r.albums(ctx, `
		WHERE EXISTS (SELECT 1 FROM music_track_files mf WHERE mf.album_id = al.id AND `+missingFile+`)
		ORDER BY al.last_search_at IS NOT NULL, al.last_search_at, al.id`)
}

// openNote says whether a migration note ("n") still explains a track that reads as
// missing: one the old table said had a file (two tracks on one path, or a file flag with
// no path), not resolved by a person, and whose track still has no file.
const openNote = `n.kind IN ('shared_path', 'no_path') AND n.resolved_at IS NULL
	AND NOT EXISTS (SELECT 1 FROM music_track_files f WHERE f.release_track_id = n.legacy_track_id)`

// OpenNotes lists an album's open migration notes (openNote), oldest first.
func (r *Repo) OpenNotes(ctx context.Context, albumID int64) ([]MigrationNote, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT n.id, COALESCE(n.legacy_track_id, 0), n.path, n.kind, n.note FROM music_migration_notes n
		 WHERE n.album_id = ? AND `+openNote+` ORDER BY n.id`, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MigrationNote
	for rows.Next() {
		var n MigrationNote
		if err := rows.Scan(&n.ID, &n.LegacyTrackID, &n.Path, &n.Kind, &n.Note); err != nil {
			return nil, err
		}
		out = append(out, n)
	}
	return out, rows.Err()
}

// ResolveNote marks one of an album's migration notes as looked at: the files are fixed,
// or the gap is accepted. The album is searched again from the next sweep.
func (r *Repo) ResolveNote(ctx context.Context, albumID, noteID int64) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE music_migration_notes SET resolved_at = CURRENT_TIMESTAMP WHERE id = ? AND album_id = ? AND resolved_at IS NULL`,
		noteID, albumID)
	return affected(res, err)
}

// FileOwner is the track a file path is recorded for: 0 when the path isn't on record, or
// is only an album extra.
func (r *Repo) FileOwner(ctx context.Context, path string) (int64, error) {
	var owner sql.NullInt64
	err := r.db.QueryRowContext(ctx, `SELECT release_track_id FROM music_track_files WHERE path = ?`, path).Scan(&owner)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return owner.Int64, err
}

// RecordSearchMiss stamps the search time and counts one more search that found nothing
// usable, which lengthens the wait before the sweep tries this album again.
func (r *Repo) RecordSearchMiss(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE albums SET last_search_at = CURRENT_TIMESTAMP, search_misses = search_misses + 1 WHERE id = ?`, id)
	return err
}

// ResetSearchMisses stamps the search time and clears the backoff after a grab.
func (r *Repo) ResetSearchMisses(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE albums SET last_search_at = CURRENT_TIMESTAMP, search_misses = 0 WHERE id = ?`, id)
	return err
}

// TouchSearch stamps the search time without counting a miss. Used when a search failed
// for a reason of our own: the album rotates behind the others instead of taking the
// first slot again every sweep, but its backoff doesn't grow.
func (r *Repo) TouchSearch(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE albums SET last_search_at = CURRENT_TIMESTAMP WHERE id = ?`, id)
	return err
}

// SetAlbumMonitored toggles an album and the tracks of its editions.
func (r *Repo) SetAlbumMonitored(ctx context.Context, id int64, monitored bool) error {
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		if _, err := tx.ExecContext(ctx, `UPDATE albums SET monitored = ? WHERE id = ?`, b2i(monitored), id); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, fmt.Sprintf(albumTracksOf, `?`), b2i(monitored), id)
		return err
	})
}

// FilePaths returns the path of every music file on record (a missing one included: it's
// still a file the library had there), for checks that ask whether a missing folder used
// to hold anything.
func (r *Repo) FilePaths(ctx context.Context) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT path FROM music_track_files WHERE path != ''`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var p string
		if err := rows.Scan(&p); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// TracksFor returns an album's target-edition tracks in disc/track order, each with its
// file, in one statement. An album with no listing yet has none.
func (r *Repo) TracksFor(ctx context.Context, albumID int64) ([]Track, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT rt.id, al.id, rt.mbid, m.position, rt.position, rt.number, rt.title, rt.length_ms, rt.monitored,
		       CASE WHEN rt.artist_credit_id IS NOT NULL
		             AND rt.artist_credit_id != COALESCE(r.artist_credit_id, al.artist_credit_id, 0)
		            THEN COALESCE((SELECT c.display FROM music_artist_credits c WHERE c.id = rt.artist_credit_id), '')
		            ELSE '' END,
		       rt.recording_id, rec.mbid,
		       COALESCE(f.id, 0), COALESCE(f.path, ''), COALESCE(f.quality, ''), COALESCE(f.bitrate_kbps, 0),
		       COALESCE(f.size_bytes, 0), COALESCE(f.source_release, ''), COALESCE(f.codec, ''),
		       COALESCE(f.lossless, 0), COALESCE(f.sample_rate_hz, 0), COALESCE(f.bit_depth, 0),
		       COALESCE(f.channels, 0), COALESCE(f.tag_state, ''), f.missing_since IS NOT NULL,
		       COALESCE(f.missing_seen_at, ''), COALESCE(f.changed_at, '')
		  FROM albums al
		  JOIN music_releases r ON r.id = al.target_release_id
		  JOIN music_media m ON m.release_id = r.id
		  JOIN music_release_tracks rt ON rt.medium_id = m.id
		  JOIN music_recordings rec ON rec.id = rt.recording_id
		  LEFT JOIN music_track_files f ON f.release_track_id = rt.id
		 WHERE al.id = ?
		 ORDER BY m.position, rt.position`, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Track
	for rows.Next() {
		var (
			t                      Track
			lengthMs               int
			mon, lossless, missing int
		)
		if err := rows.Scan(&t.ID, &t.AlbumID, &t.MBID, &t.DiscNumber, &t.TrackNumber, &t.Number, &t.Title,
			&lengthMs, &mon, &t.ArtistCredit, &t.RecordingID, &t.RecordingMBID,
			&t.FileID, &t.FilePath, &t.Format, &t.BitrateKbps, &t.SizeBytes, &t.SourceRelease, &t.Codec,
			&lossless, &t.SampleRateHz, &t.BitDepth, &t.Channels, &t.TagState, &missing,
			&t.NotFoundSince, &t.ChangedAt); err != nil {
			return nil, err
		}
		t.DurationSec = lengthMs / 1000
		t.Monitored, t.Lossless, t.Missing = mon != 0, lossless != 0, missing != 0
		t.HasFile = t.FileID != 0 && !t.Missing
		if t.Number == fmt.Sprint(t.TrackNumber) {
			t.Number = "" // just the position: nothing more to say
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// Discs lists an edition's discs (without their tracks).
func (r *Repo) Discs(ctx context.Context, editionID int64) ([]Medium, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, release_id, position, format, title, track_count FROM music_media WHERE release_id = ? ORDER BY position`, editionID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Medium
	for rows.Next() {
		var m Medium
		if err := rows.Scan(&m.ID, &m.ReleaseID, &m.Position, &m.Format, &m.Title, &m.TrackCount); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Listing is a track listing fetched for an album: the release it was read from, when
// known, and its tracks.
type Listing struct {
	ReleaseMBID string
	Tracks      []EditionTrack
}

// UpsertListing stores an album's fetched track listing on its target edition, creating
// that edition (and making it the target) when the album has none yet. A track already at
// a (disc, position) takes the listing's title, MBID and length and keeps its monitoring
// and its file: a refresh that re-listed the album must not forget which tracks are on
// disk.
func (r *Repo) UpsertListing(ctx context.Context, albumID int64, l Listing) error {
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		var target sql.NullInt64
		if err := tx.QueryRowContext(ctx, `SELECT target_release_id FROM albums WHERE id = ?`, albumID).Scan(&target); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrNotFound
			}
			return err
		}
		if target.Valid {
			return putTracksTx(ctx, tx, target.Int64, l.Tracks)
		}
		id, err := addEditionTx(ctx, tx, albumID, EditionInput{
			Edition: Edition{MBID: l.ReleaseMBID, TracksFetched: true}, Tracks: l.Tracks,
		})
		if err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE albums SET target_release_id = ? WHERE id = ?`, id, albumID)
		return err
	})
}

// UpsertTracks stores a listing given as Tracks (disc, number, title, MBID, duration):
// UpsertListing for callers that have the flat shape.
func (r *Repo) UpsertTracks(ctx context.Context, albumID int64, tracks []Track) error {
	l := Listing{Tracks: make([]EditionTrack, 0, len(tracks))}
	for _, t := range tracks {
		l.Tracks = append(l.Tracks, EditionTrack{
			Disc: t.DiscNumber, Position: t.TrackNumber, Number: t.Number, Title: t.Title, MBID: t.MBID,
			RecordingMBID: t.RecordingMBID, LengthMs: t.DurationSec * 1000,
		})
	}
	return r.UpsertListing(ctx, albumID, l)
}

// FileRecord is a file placed on (or found for) a track: where it is and what it is.
type FileRecord struct {
	Path          string
	SizeBytes     int64
	MtimeUnix     int64
	Quality       Quality // the measured tier, else the release's or the extension's (see trackTier)
	BitrateKbps   int
	SourceRelease string // "" keeps the one already recorded
	DownloadHash  string // "" keeps the one already recorded
	// Probe is what ffprobe measured, nil when it couldn't: the scan's measuring pass
	// picks the file up later (probed_at stays NULL).
	Probe *mediainfo.AudioInfo
	// Props are the facts the tag reader found in the file's headers (the library scan,
	// which runs no ffprobe): recorded like a probe's, but probed_at stays NULL so the
	// measuring pass still checks the file with ffprobe later. Ignored when Probe is set.
	Props *mediainfo.AudioInfo
	// FileID is the file's device and inode (FileIdentity), so a later scan sees a rename
	// as a move rather than a delete; "" when it isn't known.
	FileID string
	// TagState is TagStateHouse for a file written in the house style (MUS-33); "" is
	// TagStateAsFound. TagsFound is the tags it arrived with, as JSON: the audit trail,
	// and what a retag replaced.
	TagState  string
	TagsFound string
}

// ErrPathTaken is returned when a file is recorded for one track while another track
// already has it: one file is never two tracks.
var ErrPathTaken = errors.New("that file is already recorded for another track")

// RecordTrackFile records a track's file on disk: the track's file row is updated (a
// replacement) or created, at most one per track. A row already holding the path as an
// album extra becomes the track's.
func (r *Repo) RecordTrackFile(ctx context.Context, trackID int64, rec FileRecord) error {
	if strings.TrimSpace(rec.Path) == "" {
		return errors.New("music: a track file needs a path")
	}
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error { return recordTrackFileTx(ctx, tx, trackID, rec) })
}

// recordTrackFileTx is RecordTrackFile inside a transaction (the scan records an album's
// files in one).
func recordTrackFileTx(ctx context.Context, tx *store.Transaction, trackID int64, rec FileRecord) error {
	var albumID int64
	if err := tx.QueryRowContext(ctx, `
		SELECT r.album_id FROM music_release_tracks rt
		  JOIN music_media m ON m.id = rt.medium_id
		  JOIN music_releases r ON r.id = m.release_id
		 WHERE rt.id = ?`, trackID).Scan(&albumID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	var byTrack, byPath, pathTrack sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT id FROM music_track_files WHERE release_track_id = ?`, trackID).Scan(&byTrack); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if err := tx.QueryRowContext(ctx, `SELECT id, release_track_id FROM music_track_files WHERE path = ?`, rec.Path).Scan(&byPath, &pathTrack); err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	if byPath.Valid && pathTrack.Valid && pathTrack.Int64 != trackID {
		return ErrPathTaken
	}
	rowID := byTrack
	if byTrack.Valid && byPath.Valid && byPath.Int64 != byTrack.Int64 {
		// The new path was an extra of this album: it is this track's file now, so
		// its row folds into the track's (rows only; the file stays where it is).
		if _, err := tx.ExecContext(ctx, `DELETE FROM music_track_files WHERE id = ?`, byPath.Int64); err != nil {
			return err
		}
	} else if !byTrack.Valid {
		rowID = byPath
	}
	if !rowID.Valid {
		if err := tx.QueryRowContext(ctx,
			`INSERT INTO music_track_files (album_id, release_track_id, path) VALUES (?, ?, ?) RETURNING id`,
			albumID, trackID, rec.Path).Scan(&rowID); err != nil {
			return err
		}
	}
	q := HeldTier(string(rec.Quality), rec.BitrateKbps)
	lossless := IsLosslessQuality(q)
	var p mediainfo.AudioInfo
	probed := rec.Probe != nil
	switch {
	case probed:
		p = *rec.Probe
	case rec.Props != nil:
		p = *rec.Props
	}
	_, err := tx.ExecContext(ctx, `
		UPDATE music_track_files SET
		  album_id = ?, release_track_id = ?, path = ?, size_bytes = ?, mtime_unix = ?, file_id = ?, container = ?,
		  lossless = ?, quality = ?, bitrate_kbps = ?, codec = ?, sample_rate_hz = ?, bit_depth = ?,
		  channels = ?, duration_ms = ?,
		  probed_at = CASE WHEN ? = 1 THEN CURRENT_TIMESTAMP ELSE NULL END,
		  source_release = CASE WHEN ? != '' THEN ? ELSE source_release END,
		  download_hash = CASE WHEN ? != '' THEN ? ELSE download_hash END,
		  tags_json = ?, tag_state = ?,
		  tagged_at = CASE WHEN ? = 'house' THEN CURRENT_TIMESTAMP ELSE NULL END, missing_since = NULL,
		  missing_seen_at = NULL, changed_at = NULL,
		  added_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		albumID, trackID, rec.Path, rec.SizeBytes, rec.MtimeUnix, rec.FileID, containerOf(rec.Path),
		b2i(lossless), string(rec.Quality), rec.BitrateKbps, p.Codec, p.SampleRateHz, p.BitsPerSample,
		p.Channels, int(p.DurationSec*1000), b2i(probed),
		rec.SourceRelease, rec.SourceRelease, rec.DownloadHash, rec.DownloadHash,
		rec.TagsFound, tagState(rec.TagState), tagState(rec.TagState), rowID.Int64)
	return err
}

// containerOf is a file's container, from its extension ("flac", "mp3").
func containerOf(path string) string {
	return strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
}

// SetTrackFile records a track's file with just its tier, bitrate and size: RecordTrackFile
// without a measurement.
func (r *Repo) SetTrackFile(ctx context.Context, trackID int64, path, format string, bitrate int, size int64, sourceRelease string) error {
	return r.RecordTrackFile(ctx, trackID, FileRecord{
		Path: path, SizeBytes: size, Quality: Quality(format), BitrateKbps: bitrate, SourceRelease: sourceRelease,
	})
}

// HeldTiers is the lowest tier among an album's tracks that have a file — the floor a
// new release must reach before it's worth grabbing (see HeldTier). ok is false when no
// track has a file. A held file whose tier is unknown makes the floor unknown: nothing is
// refused on a guess.
func (r *Repo) HeldTiers(ctx context.Context, albumID int64) (lowest Quality, ok bool, err error) {
	tracks, err := r.TracksFor(ctx, albumID)
	if err != nil {
		return QualityUnknown, false, err
	}
	lowest, ok = lowestHeld(tracks)
	return lowest, ok, nil
}

// lowestHeld is the lowest tier among the tracks that have a file (unknown ranks lowest
// of all); ok is false when none has one.
func lowestHeld(tracks []Track) (lowest Quality, ok bool) {
	for _, t := range tracks {
		if !t.HasFile {
			continue
		}
		q := HeldTier(t.Format, t.BitrateKbps)
		if !ok || QualityRank(q) < QualityRank(lowest) {
			lowest = q
		}
		ok = true
	}
	return lowest, ok
}

// UnmeasuredFiles lists up to limit files, after file afterID, that haven't been measured
// with every fact yet: files the migration copied, ones recorded while ffprobe couldn't
// run, or by a scan that couldn't probe them. The cursor lets a pass move past files that
// can't be measured.
func (r *Repo) UnmeasuredFiles(ctx context.Context, afterID int64, limit int) ([]TrackFile, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+fileCols+` FROM music_track_files
		  WHERE probed_at IS NULL AND missing_since IS NULL AND path != '' AND id > ?
		  ORDER BY id LIMIT ?`, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []TrackFile
	for rows.Next() {
		f, err := scanFile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SetFileMeasured records a held file's measured tier and facts, leaving where it is and
// where it came from alone. Guarded on the path, so a file replaced meanwhile isn't
// stamped with the old one's measurements.
func (r *Repo) SetFileMeasured(ctx context.Context, fileID int64, path string, q Quality, info mediainfo.AudioInfo) error {
	_, err := r.db.ExecContext(ctx, `
		UPDATE music_track_files SET quality = ?, bitrate_kbps = ?, lossless = ?, codec = ?, sample_rate_hz = ?,
		       bit_depth = ?, channels = ?, duration_ms = ?, probed_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND path = ?`,
		string(q), info.BitrateKbps, b2i(IsLosslessQuality(q)), info.Codec, info.SampleRateHz, info.BitsPerSample,
		info.Channels, int(info.DurationSec*1000), fileID, path)
	return err
}

// ClearTrackFile flips a track back to wanted. Its file row stays, as an album extra:
// the file itself is wherever it was, and a row is never the reason a file is forgotten.
func (r *Repo) ClearTrackFile(ctx context.Context, trackID int64) error {
	res, err := r.db.ExecContext(ctx, `UPDATE music_track_files SET release_track_id = NULL WHERE release_track_id = ?`, trackID)
	return affected(res, err)
}

// AddEvent appends an artist timeline event (best effort).
func (r *Repo) AddEvent(ctx context.Context, artistID int64, event, detail string) {
	_, _ = r.db.ExecContext(ctx,
		`INSERT INTO artist_events (artist_id, event, detail) VALUES (?, ?, ?)`, artistID, event, detail)
}

// Events returns an artist's timeline, newest first.
func (r *Repo) Events(ctx context.Context, artistID int64, limit int) ([]Event, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT event, detail, created_at FROM artist_events WHERE artist_id = ? ORDER BY id DESC LIMIT ?`,
		artistID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Event
	for rows.Next() {
		var e Event
		if err := rows.Scan(&e.Event, &e.Detail, &e.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
