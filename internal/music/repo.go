package music

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// Repo persists artists, albums and tracks in SQLite.
type Repo struct{ db *sql.DB }

// NewRepo builds a repository over the given pool.
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

const artistCols = `id, mbid, name, sort_name, overview, image_url, genres_json,
	monitored, quality_profile, added_at`

func scanArtist(row interface{ Scan(...any) error }) (Artist, error) {
	var (
		a          Artist
		genresJSON string
		mon        int
	)
	if err := row.Scan(&a.ID, &a.MBID, &a.Name, &a.SortName, &a.Overview, &a.ImageURL,
		&genresJSON, &mon, &a.QualityProfile, &a.AddedAt); err != nil {
		return Artist{}, err
	}
	a.Monitored = mon != 0
	if genresJSON != "" {
		_ = json.Unmarshal([]byte(genresJSON), &a.Genres)
	}
	return a, nil
}

// ListArtists returns every artist with its holdings summary, newest first.
func (r *Repo) ListArtists(ctx context.Context) ([]Artist, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+artistCols+` FROM artists ORDER BY added_at DESC, id DESC`)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stats, err := r.artistStats(ctx)
	if err != nil {
		return nil, err
	}
	for i := range out {
		if s, ok := stats[out[i].ID]; ok {
			out[i].Stats = &s
		} else {
			out[i].Stats = &ArtistStats{}
		}
	}
	return out, nil
}

// artistStats summarizes albums/tracks/size per artist in one grouped query, so listing the
// library doesn't run a per-artist count.
func (r *Repo) artistStats(ctx context.Context) (map[int64]ArtistStats, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT al.artist_id,
		       COUNT(DISTINCT al.id),
		       COUNT(t.id),
		       SUM(CASE WHEN t.has_file = 1 THEN 1 ELSE 0 END),
		       SUM(COALESCE(t.size_bytes, 0))
		  FROM albums al LEFT JOIN tracks t ON t.album_id = al.id
		 GROUP BY al.artist_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]ArtistStats{}
	for rows.Next() {
		var id int64
		var s ArtistStats
		var have, size sql.NullInt64
		if err := rows.Scan(&id, &s.Albums, &s.Tracks, &have, &size); err != nil {
			return nil, err
		}
		s.HaveTracks, s.SizeBytes = int(have.Int64), size.Int64
		out[id] = s
	}
	return out, rows.Err()
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

// CreateArtist inserts an artist row.
func (r *Repo) CreateArtist(ctx context.Context, a Artist) (Artist, error) {
	genresJSON := ""
	if len(a.Genres) > 0 {
		if raw, err := json.Marshal(a.Genres); err == nil {
			genresJSON = string(raw)
		}
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO artists (mbid, name, sort_name, overview, image_url, genres_json, monitored, quality_profile)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		a.MBID, a.Name, a.SortName, a.Overview, a.ImageURL, genresJSON, b2i(a.Monitored), a.QualityProfile)
	if err != nil {
		if isUnique(err) {
			return Artist{}, ErrExists
		}
		return Artist{}, err
	}
	id, _ := res.LastInsertId()
	return r.GetArtist(ctx, id)
}

// UpdateArtistMeta refreshes provider-owned artist fields, leaving the user's choices alone.
func (r *Repo) UpdateArtistMeta(ctx context.Context, id int64, overview, imageURL string, genres []string) error {
	genresJSON := ""
	if len(genres) > 0 {
		if raw, err := json.Marshal(genres); err == nil {
			genresJSON = string(raw)
		}
	}
	_, err := r.db.ExecContext(ctx,
		`UPDATE artists SET overview = ?, image_url = ?, genres_json = ? WHERE id = ?`,
		overview, imageURL, genresJSON, id)
	return err
}

// SetArtistMonitored toggles an artist and cascades to its albums and tracks, so an
// unmonitored artist can't leave monitored albums behind that the searcher keeps chasing.
func (r *Repo) SetArtistMonitored(ctx context.Context, id int64, monitored bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `UPDATE artists SET monitored = ? WHERE id = ?`, b2i(monitored), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE albums SET monitored = ? WHERE artist_id = ?`, b2i(monitored), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx,
		`UPDATE tracks SET monitored = ? WHERE album_id IN (SELECT id FROM albums WHERE artist_id = ?)`,
		b2i(monitored), id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetArtistQualityProfile changes an artist's profile.
func (r *Repo) SetArtistQualityProfile(ctx context.Context, id int64, profile string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE artists SET quality_profile = ? WHERE id = ?`, profile, id)
	return affected(res, err)
}

// DeleteArtist removes an artist; albums and tracks cascade.
func (r *Repo) DeleteArtist(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM artists WHERE id = ?`, id)
	return affected(res, err)
}

const albumCols = `id, artist_id, mbid, title, year, album_type, cover_url, release_date, monitored, added_at`

func scanAlbum(row interface{ Scan(...any) error }) (Album, error) {
	var (
		al  Album
		mon int
	)
	if err := row.Scan(&al.ID, &al.ArtistID, &al.MBID, &al.Title, &al.Year, &al.AlbumType,
		&al.CoverURL, &al.ReleaseDate, &mon, &al.AddedAt); err != nil {
		return Album{}, err
	}
	al.Monitored = mon != 0
	return al, nil
}

// AlbumsFor returns an artist's albums, newest first, each with its track counts.
func (r *Repo) AlbumsFor(ctx context.Context, artistID int64) ([]Album, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+albumCols+` FROM albums WHERE artist_id = ? ORDER BY year DESC, title`, artistID)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		if err := r.fillAlbumCounts(ctx, &out[i]); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (r *Repo) fillAlbumCounts(ctx context.Context, al *Album) error {
	var have, size sql.NullInt64
	return r.db.QueryRowContext(ctx,
		`SELECT COUNT(*), SUM(CASE WHEN has_file = 1 THEN 1 ELSE 0 END), SUM(COALESCE(size_bytes, 0))
		   FROM tracks WHERE album_id = ?`, al.ID).
		Scan(&al.TrackCount, &have, &size)
}

// GetAlbum returns one album by id (without tracks).
func (r *Repo) GetAlbum(ctx context.Context, id int64) (Album, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+albumCols+` FROM albums WHERE id = ?`, id)
	al, err := scanAlbum(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Album{}, ErrNotFound
	}
	if err != nil {
		return Album{}, err
	}
	err = r.fillAlbumCounts(ctx, &al)
	return al, err
}

// UpsertAlbum inserts an album, or updates the provider-owned fields when its MBID is
// already present — so a metadata refresh corrects a title or cover without creating a
// duplicate row or disturbing the user's monitoring choice.
func (r *Repo) UpsertAlbum(ctx context.Context, al Album) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO albums (artist_id, mbid, title, year, album_type, cover_url, release_date, monitored)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(mbid) DO UPDATE SET
		   title = excluded.title, year = excluded.year, album_type = excluded.album_type,
		   cover_url = excluded.cover_url, release_date = excluded.release_date`,
		al.ArtistID, al.MBID, al.Title, al.Year, al.AlbumType, al.CoverURL, al.ReleaseDate, b2i(al.Monitored))
	if err != nil {
		return 0, err
	}
	if id, _ := res.LastInsertId(); id > 0 {
		return id, nil
	}
	var id int64
	err = r.db.QueryRowContext(ctx, `SELECT id FROM albums WHERE mbid = ?`, al.MBID).Scan(&id)
	return id, err
}

// SetAlbumMonitored toggles an album and its tracks.
func (r *Repo) SetAlbumMonitored(ctx context.Context, id int64, monitored bool) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE albums SET monitored = ? WHERE id = ?`, b2i(monitored), id); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `UPDATE tracks SET monitored = ? WHERE album_id = ?`, b2i(monitored), id)
	return err
}

const trackCols = `id, album_id, mbid, disc_number, track_number, title, duration_sec,
	monitored, has_file, file_path, format, bitrate_kbps, size_bytes, source_release`

// TracksFor returns an album's tracks in disc/track order.
func (r *Repo) TracksFor(ctx context.Context, albumID int64) ([]Track, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT `+trackCols+` FROM tracks WHERE album_id = ? ORDER BY disc_number, track_number`, albumID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Track
	for rows.Next() {
		var t Track
		var mon, hf int
		if err := rows.Scan(&t.ID, &t.AlbumID, &t.MBID, &t.DiscNumber, &t.TrackNumber, &t.Title,
			&t.DurationSec, &mon, &hf, &t.FilePath, &t.Format, &t.BitrateKbps, &t.SizeBytes, &t.SourceRelease); err != nil {
			return nil, err
		}
		t.Monitored, t.HasFile = mon != 0, hf != 0
		out = append(out, t)
	}
	return out, rows.Err()
}

// UpsertTracks replaces an album's track listing with the metadata's, preserving the file
// state and monitoring already recorded against each (disc, track) slot. A refresh that
// re-listed the album must not forget which tracks are on disk.
func (r *Repo) UpsertTracks(ctx context.Context, albumID int64, tracks []Track) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	for _, t := range tracks {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO tracks (album_id, mbid, disc_number, track_number, title, duration_sec, monitored)
			 VALUES (?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(album_id, disc_number, track_number) DO UPDATE SET
			   mbid = excluded.mbid, title = excluded.title, duration_sec = excluded.duration_sec`,
			albumID, t.MBID, t.DiscNumber, t.TrackNumber, t.Title, t.DurationSec, b2i(true)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SetTrackFile records a track's file on disk.
func (r *Repo) SetTrackFile(ctx context.Context, trackID int64, path, format string, bitrate int, size int64, sourceRelease string) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE tracks SET has_file = 1, file_path = ?, format = ?, bitrate_kbps = ?, size_bytes = ?,
		        source_release = CASE WHEN ? != '' THEN ? ELSE source_release END
		  WHERE id = ?`,
		path, format, bitrate, size, sourceRelease, sourceRelease, trackID)
	return affected(res, err)
}

// ClearTrackFile flips a track back to wanted.
func (r *Repo) ClearTrackFile(ctx context.Context, trackID int64) error {
	res, err := r.db.ExecContext(ctx,
		`UPDATE tracks SET has_file = 0, file_path = '', format = '', bitrate_kbps = 0, size_bytes = 0 WHERE id = ?`,
		trackID)
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

func isUnique(err error) bool {
	return err != nil && strings.Contains(strings.ToLower(err.Error()), "unique")
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
