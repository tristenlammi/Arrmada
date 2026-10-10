package movies

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"

	"github.com/tristenlammi/arrmada/internal/store"
)

// ErrNotFound is returned when a movie id doesn't exist.
var ErrNotFound = errors.New("movie not found")

// ErrExists is returned when a TMDB movie is already in the library.
var ErrExists = errors.New("movie already in library")

// Repo persists movies in SQLite.
type Repo struct {
	db *sql.DB
	// tx, when set, is the transaction every statement runs in instead of the pool (see
	// inTx). A Repo bound to one is only used inside that transaction's function.
	tx *sql.Tx
}

// NewRepo builds a repository over the given pool.
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

// q is where statements run: the bound transaction, or the pool.
func (r *Repo) q() store.Execer {
	if r.tx != nil {
		return r.tx
	}
	return r.db
}

// inTx runs fn with a copy of the repo whose statements all go through one transaction,
// committed when fn returns nil and rolled back otherwise.
func (r *Repo) inTx(ctx context.Context, fn func(tx *sql.Tx, r *Repo) error) error {
	if r.tx != nil {
		return fn(r.tx, r) // already inside one
	}
	return store.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		return fn(tx, &Repo{db: r.db, tx: tx})
	})
}

const movieCols = `id, tmdb_id, imdb_id, title, year, overview, poster_url, runtime, status,
	monitored, quality_profile, min_availability, has_file, movie_file_path, added_at, extra_json, media_json,
	source_release, upgrade_hold, converted_from_release, converted_from_size`

func (r *Repo) scan(row interface{ Scan(...any) error }) (Movie, error) {
	var (
		m                    Movie
		mon, hf, hold        int
		extraJSON, mediaJSON string
	)
	err := row.Scan(&m.ID, &m.TMDBID, &m.IMDBID, &m.Title, &m.Year, &m.Overview, &m.PosterURL,
		&m.Runtime, &m.Status, &mon, &m.QualityProfile, &m.MinAvailability, &hf, &m.MovieFilePath,
		&m.AddedAt, &extraJSON, &mediaJSON, &m.SourceRelease, &hold, &m.ConvertedFromRelease, &m.ConvertedFromSize)
	if err != nil {
		return Movie{}, err
	}
	m.Monitored = mon != 0
	m.HasFile = hf != 0
	m.UpgradeHold = hold != 0
	if extraJSON != "" {
		var ex MovieExtra
		if json.Unmarshal([]byte(extraJSON), &ex) == nil {
			m.Extra = &ex
		}
	}
	if m.HasFile && mediaJSON != "" {
		var f MovieFile
		if json.Unmarshal([]byte(mediaJSON), &f) == nil {
			m.File = &f
		}
	}
	return m, nil
}

// SetMediaInfo caches the default file's media info as JSON.
func (r *Repo) SetMediaInfo(ctx context.Context, id int64, mediaJSON string) error {
	_, err := r.q().ExecContext(ctx, `UPDATE movies SET media_json = ? WHERE id = ?`, mediaJSON, id)
	return err
}

// SetMediaInfoForPath is SetMediaInfo only while the movie still holds the file at path, for
// background reads that may finish after an import replaced it.
func (r *Repo) SetMediaInfoForPath(ctx context.Context, id int64, path, mediaJSON string) error {
	_, err := r.q().ExecContext(ctx,
		`UPDATE movies SET media_json = ? WHERE id = ? AND has_file = 1 AND movie_file_path = ?`, mediaJSON, id, path)
	return err
}

// ListSummaries returns every movie as the library list shows it, newest first: only the
// columns the list needs, the rating pulled out of extra_json in SQL (the cast, overview
// and the rest of it never leave the database) and the media facts from media_json.
func (r *Repo) ListSummaries(ctx context.Context) ([]MovieSummary, error) {
	rows, err := r.q().QueryContext(ctx, `SELECT id, title, year, poster_url, monitored, has_file, quality_profile,
		min_availability, added_at, COALESCE(CASE WHEN json_valid(extra_json) THEN json_extract(extra_json, '$.vote_average') END, 0), media_json
		FROM movies ORDER BY added_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MovieSummary{}
	for rows.Next() {
		var (
			s         MovieSummary
			mon, hf   int
			vote      float64
			mediaJSON string
		)
		if err := rows.Scan(&s.ID, &s.Title, &s.Year, &s.PosterURL, &mon, &hf, &s.QualityProfile,
			&s.MinAvailability, &s.AddedAt, &vote, &mediaJSON); err != nil {
			return nil, err
		}
		s.Monitored, s.HasFile, s.VoteAverage = mon != 0, hf != 0, vote
		s.SortTitle = SortTitle(s.Title)
		if s.HasFile {
			var f *MovieFile
			if mediaJSON != "" {
				var mf MovieFile
				if json.Unmarshal([]byte(mediaJSON), &mf) == nil {
					f = &mf
				}
			}
			s.Media = summaryMedia(f)
			if f != nil {
				s.SizeBytes, s.FileMissing = f.SizeBytes, f.Missing
			}
			// The same test as Movie.MediaStale.
			s.mediaStale = f == nil || (!f.Missing && f.MediaVersion < MediaVersion)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// List returns all movies, newest first.
func (r *Repo) List(ctx context.Context) ([]Movie, error) {
	rows, err := r.q().QueryContext(ctx, `SELECT `+movieCols+` FROM movies ORDER BY added_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Movie
	for rows.Next() {
		m, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// ExistingTMDBIDs returns the set of TMDB ids already in the library, for
// dedup (e.g. marking which collection members are already added).
func (r *Repo) ExistingTMDBIDs(ctx context.Context) (map[int]bool, error) {
	rows, err := r.q().QueryContext(ctx, `SELECT tmdb_id FROM movies`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var id int
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out[id] = true
	}
	return out, rows.Err()
}

// SearchTargets returns the movies that have a monitored track without a file: a
// monitored movie still missing its main file, or any movie with a monitored extra
// version still missing its file (an extra track is searched even when the movie row
// itself is unmonitored, as the sweeps always did). Ordered like List.
func (r *Repo) SearchTargets(ctx context.Context) ([]Movie, error) {
	return r.listWhere(ctx, `(monitored = 1 AND has_file = 0)
		OR EXISTS (SELECT 1 FROM movie_versions v WHERE v.movie_id = movies.id AND v.monitored = 1 AND v.has_file = 0)`)
}

// UpgradeTargets returns the monitored movies that have a file. Ordered like List.
func (r *Repo) UpgradeTargets(ctx context.Context) ([]Movie, error) {
	return r.listWhere(ctx, `monitored = 1 AND has_file = 1`)
}

// listWhere lists the movies matching a fixed WHERE clause (never user input), newest first.
func (r *Repo) listWhere(ctx context.Context, where string) ([]Movie, error) {
	rows, err := r.q().QueryContext(ctx, `SELECT `+movieCols+` FROM movies WHERE `+where+` ORDER BY added_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Movie
	for rows.Next() {
		m, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Get returns one movie by id.
func (r *Repo) Get(ctx context.Context, id int64) (Movie, error) {
	row := r.q().QueryRowContext(ctx, `SELECT `+movieCols+` FROM movies WHERE id = ?`, id)
	m, err := r.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Movie{}, ErrNotFound
	}
	return m, err
}

// GetByTMDB returns the library movie with a TMDB id (ErrNotFound when there is none).
func (r *Repo) GetByTMDB(ctx context.Context, tmdbID int) (Movie, error) {
	row := r.q().QueryRowContext(ctx, `SELECT `+movieCols+` FROM movies WHERE tmdb_id = ?`, tmdbID)
	m, err := r.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Movie{}, ErrNotFound
	}
	return m, err
}

// ByTMDBIDs returns the library movies with these TMDB ids, in one query.
func (r *Repo) ByTMDBIDs(ctx context.Context, tmdbIDs []int) ([]Movie, error) {
	if len(tmdbIDs) == 0 {
		return nil, nil
	}
	args := make([]any, len(tmdbIDs))
	for i, id := range tmdbIDs {
		args[i] = id
	}
	rows, err := r.q().QueryContext(ctx, `SELECT `+movieCols+` FROM movies WHERE tmdb_id IN (`+
		strings.TrimSuffix(strings.Repeat("?,", len(tmdbIDs)), ",")+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Movie
	for rows.Next() {
		m, err := r.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

// Create inserts a movie.
func (r *Repo) Create(ctx context.Context, m Movie) (Movie, error) {
	if m.MinAvailability == "" {
		m.MinAvailability = "released"
	}
	extraJSON := ""
	if m.Extra != nil {
		if b, err := json.Marshal(m.Extra); err == nil {
			extraJSON = string(b)
		}
	}
	res, err := r.q().ExecContext(ctx,
		`INSERT INTO movies (tmdb_id, imdb_id, title, year, overview, poster_url, runtime, status,
			monitored, quality_profile, min_availability, extra_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		m.TMDBID, m.IMDBID, m.Title, m.Year, m.Overview, m.PosterURL, m.Runtime, m.Status,
		boolToInt(m.Monitored), m.QualityProfile, m.MinAvailability, extraJSON)
	if err != nil {
		if isUnique(err) {
			return Movie{}, ErrExists
		}
		return Movie{}, err
	}
	id, _ := res.LastInsertId()
	return r.Get(ctx, id)
}

// Delete removes a movie by id, with its history and extra versions, in one transaction.
// Those tables have no foreign keys, so without this a deleted movie left its timeline
// and version rows behind, waiting to attach to whatever reused the id.
func (r *Repo) Delete(ctx context.Context, id int64) error {
	return r.inTx(ctx, func(tx *sql.Tx, _ *Repo) error {
		res, err := tx.ExecContext(ctx, `DELETE FROM movies WHERE id = ?`, id)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n == 0 {
			return ErrNotFound
		}
		if _, err := tx.ExecContext(ctx, `DELETE FROM movie_versions WHERE movie_id = ?`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `DELETE FROM movie_events WHERE movie_id = ?`, id)
		return err
	})
}

// SetMonitored toggles monitoring.
func (r *Repo) SetMonitored(ctx context.Context, id int64, monitored bool) error {
	_, err := r.q().ExecContext(ctx, `UPDATE movies SET monitored = ? WHERE id = ?`, boolToInt(monitored), id)
	return err
}

// SearchState returns when the movie was last swept and how many consecutive sweeps
// grabbed nothing (drives the search backoff).
func (r *Repo) SearchState(ctx context.Context, movieID int64) (lastSearchAt string, misses int) {
	_ = r.q().QueryRowContext(ctx,
		`SELECT last_search_at, search_misses FROM movies WHERE id = ?`, movieID).Scan(&lastSearchAt, &misses)
	return lastSearchAt, misses
}

// SearchStamp is where one movie stands on the missing-sweep's backoff.
type SearchStamp struct {
	LastAt string // when the sweep last searched it, as stored ("" = never)
	Misses int    // sweeps in a row that grabbed nothing
}

// SearchStates is every movie's search state in one query, for a list page.
func (r *Repo) SearchStates(ctx context.Context) (map[int64]SearchStamp, error) {
	rows, err := r.q().QueryContext(ctx, `SELECT id, last_search_at, search_misses FROM movies`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]SearchStamp{}
	for rows.Next() {
		var id int64
		var st SearchStamp
		if err := rows.Scan(&id, &st.LastAt, &st.Misses); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// SearchStatesFor is SearchStates for these ids only.
func (r *Repo) SearchStatesFor(ctx context.Context, ids []int64) (map[int64]SearchStamp, error) {
	out := map[int64]SearchStamp{}
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := r.q().QueryContext(ctx,
		`SELECT id, last_search_at, search_misses FROM movies WHERE id IN (?`+strings.Repeat(",?", len(ids)-1)+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var st SearchStamp
		if err := rows.Scan(&id, &st.LastAt, &st.Misses); err != nil {
			return nil, err
		}
		out[id] = st
	}
	return out, rows.Err()
}

// RecordSearchMiss stamps the sweep time and increments the miss counter.
func (r *Repo) RecordSearchMiss(ctx context.Context, movieID int64) {
	_, _ = r.q().ExecContext(ctx,
		`UPDATE movies SET last_search_at = datetime('now'), search_misses = search_misses + 1 WHERE id = ?`, movieID)
}

// ResetSearchMisses clears the backoff after a successful grab.
func (r *Repo) ResetSearchMisses(ctx context.Context, movieID int64) {
	_, _ = r.q().ExecContext(ctx,
		`UPDATE movies SET last_search_at = datetime('now'), search_misses = 0 WHERE id = ?`, movieID)
}

// SetFile marks a movie as having a file at path.
func (r *Repo) SetFile(ctx context.Context, id int64, path string) error {
	_, err := r.q().ExecContext(ctx, `UPDATE movies SET has_file = 1, movie_file_path = ? WHERE id = ?`, path, id)
	return err
}

// ClearFile marks a movie as having no file (after its file is deleted).
func (r *Repo) ClearFile(ctx context.Context, id int64) error {
	_, err := r.q().ExecContext(ctx, `UPDATE movies SET has_file = 0, movie_file_path = '', media_json = '', source_release = '', upgrade_hold = 0,
		converted_from_release = '', converted_from_size = 0 WHERE id = ?`, id)
	return err
}

// SetConvertedFromForPath records, on every track of a movie whose file is at path, the
// release and size the file had before Convert first shrank it. The release is the
// track's recorded source release as it stands — call this BEFORE the repoint restamps
// its codec. A track that already has a baseline keeps it: a re-conversion's "before" is
// itself a conversion, and upgrades must beat the first original.
func (r *Repo) SetConvertedFromForPath(ctx context.Context, movieID int64, path string, size int64) error {
	return r.inTx(ctx, func(_ *sql.Tx, r *Repo) error {
		if _, err := r.q().ExecContext(ctx,
			`UPDATE movies SET converted_from_release = source_release, converted_from_size = ?
			  WHERE id = ? AND has_file = 1 AND movie_file_path = ?
			    AND converted_from_release = '' AND converted_from_size = 0`, size, movieID, path); err != nil {
			return err
		}
		_, err := r.q().ExecContext(ctx,
			`UPDATE movie_versions SET converted_from_release = source_release, converted_from_size = ?
			  WHERE movie_id = ? AND has_file = 1 AND file_path = ?
			    AND converted_from_release = '' AND converted_from_size = 0`, size, movieID, path)
		return err
	})
}

// ClearConvertedFrom forgets a track's pre-conversion baseline: a new file was imported
// into it. versionID 0 is the default track (the movie row).
func (r *Repo) ClearConvertedFrom(ctx context.Context, movieID, versionID int64) error {
	if versionID == 0 {
		_, err := r.q().ExecContext(ctx,
			`UPDATE movies SET converted_from_release = '', converted_from_size = 0 WHERE id = ?`, movieID)
		return err
	}
	_, err := r.q().ExecContext(ctx,
		`UPDATE movie_versions SET converted_from_release = '', converted_from_size = 0 WHERE id = ?`, versionID)
	return err
}

// SetSourceRelease records the release name the default file was imported from
// (used to score the current file when deciding upgrades).
func (r *Repo) SetSourceRelease(ctx context.Context, id int64, release string) error {
	_, err := r.q().ExecContext(ctx, `UPDATE movies SET source_release = ? WHERE id = ?`, release, id)
	return err
}

// SetVersionSourceRelease records the release name an extra version's file came from.
func (r *Repo) SetVersionSourceRelease(ctx context.Context, id int64, release string) error {
	_, err := r.q().ExecContext(ctx, `UPDATE movie_versions SET source_release = ? WHERE id = ?`, release, id)
	return err
}

// SetQualityProfile changes a movie's quality profile. Moving to another profile ends an
// upgrade hold: the hold was about the old profile's change (SQLite reads quality_profile
// in the CASE before this statement changes it).
func (r *Repo) SetQualityProfile(ctx context.Context, id int64, profile string) error {
	res, err := r.q().ExecContext(ctx,
		`UPDATE movies SET upgrade_hold = CASE WHEN quality_profile = ? THEN upgrade_hold ELSE 0 END,
			quality_profile = ? WHERE id = ?`, profile, profile, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetMinAvailability changes when a movie becomes eligible for searching.
func (r *Repo) SetMinAvailability(ctx context.Context, id int64, avail string) error {
	res, err := r.q().ExecContext(ctx, `UPDATE movies SET min_availability = ? WHERE id = ?`, avail, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// UpdateMetadata refreshes the core + enriched metadata fields (from a provider
// re-fetch), leaving library/monitoring state untouched.
func (r *Repo) UpdateMetadata(ctx context.Context, id int64, m Movie) error {
	extraJSON := ""
	if m.Extra != nil {
		if b, err := json.Marshal(m.Extra); err == nil {
			extraJSON = string(b)
		}
	}
	_, err := r.q().ExecContext(ctx,
		`UPDATE movies SET imdb_id = ?, title = ?, year = ?, overview = ?, poster_url = ?,
			runtime = ?, status = ?, extra_json = ? WHERE id = ?`,
		m.IMDBID, m.Title, m.Year, m.Overview, m.PosterURL, m.Runtime, m.Status, extraJSON, id)
	return err
}

// --- extra version tracks -------------------------------------------------

const versionCols = `id, movie_id, label, quality_profile, edition, monitored, has_file, file_path, size_bytes, source_release, upgrade_hold,
	converted_from_release, converted_from_size, media_json`

func scanVersion(row interface{ Scan(...any) error }) (Version, int64, error) {
	var (
		v             Version
		movieID       int64
		mon, hf, hold int
		mediaJSON     string
	)
	err := row.Scan(&v.ID, &movieID, &v.Label, &v.QualityProfile, &v.Edition, &mon, &hf, &v.FilePath, &v.SizeBytes, &v.SourceRelease, &hold,
		&v.ConvertedFromRelease, &v.ConvertedFromSize, &mediaJSON)
	if err != nil {
		return Version{}, 0, err
	}
	v.Monitored = mon != 0
	v.HasFile = hf != 0
	v.UpgradeHold = hold != 0
	// The track's cached media info, as the default track's comes from movies.media_json.
	if v.HasFile && mediaJSON != "" {
		var f MovieFile
		if json.Unmarshal([]byte(mediaJSON), &f) == nil {
			v.File = &f
		}
	}
	return v, movieID, err
}

// ListVersions returns the extra version tracks for a movie.
func (r *Repo) ListVersions(ctx context.Context, movieID int64) ([]Version, error) {
	rows, err := r.q().QueryContext(ctx, `SELECT `+versionCols+` FROM movie_versions WHERE movie_id = ? ORDER BY id`, movieID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Version
	for rows.Next() {
		v, _, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

// ListAllVersions returns every movie's extra version tracks, keyed by movie id, in one
// query — for whole-library passes that would otherwise ask once per movie.
func (r *Repo) ListAllVersions(ctx context.Context) (map[int64][]Version, error) {
	rows, err := r.q().QueryContext(ctx, `SELECT `+versionCols+` FROM movie_versions ORDER BY movie_id, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64][]Version{}
	for rows.Next() {
		v, movieID, err := scanVersion(rows)
		if err != nil {
			return nil, err
		}
		out[movieID] = append(out[movieID], v)
	}
	return out, rows.Err()
}

// GetVersion returns one extra version plus its movie id.
func (r *Repo) GetVersion(ctx context.Context, id int64) (Version, int64, error) {
	row := r.q().QueryRowContext(ctx, `SELECT `+versionCols+` FROM movie_versions WHERE id = ?`, id)
	v, movieID, err := scanVersion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, 0, ErrNotFound
	}
	return v, movieID, err
}

// CreateVersion adds an extra version track.
func (r *Repo) CreateVersion(ctx context.Context, movieID int64, v Version) (Version, error) {
	res, err := r.q().ExecContext(ctx,
		`INSERT INTO movie_versions (movie_id, label, quality_profile, edition, monitored)
		 VALUES (?, ?, ?, ?, ?)`,
		movieID, v.Label, v.QualityProfile, v.Edition, boolToInt(v.Monitored))
	if err != nil {
		return Version{}, err
	}
	id, _ := res.LastInsertId()
	out, _, err := r.GetVersion(ctx, id)
	return out, err
}

// UpdateVersion writes a version's mutable fields.
func (r *Repo) UpdateVersion(ctx context.Context, id int64, label, profile, edition string, monitored bool) error {
	// A new profile ends the track's upgrade hold, as it does for the movie (SetQualityProfile).
	res, err := r.q().ExecContext(ctx,
		`UPDATE movie_versions SET upgrade_hold = CASE WHEN quality_profile = ? THEN upgrade_hold ELSE 0 END,
			label = ?, quality_profile = ?, edition = ?, monitored = ? WHERE id = ?`,
		profile, label, profile, edition, boolToInt(monitored), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetVersionFile records a file for an extra version with its cached media info ("" = none
// read yet; the detail page reads it in the background).
func (r *Repo) SetVersionFile(ctx context.Context, id int64, path string, size int64, mediaJSON string) error {
	_, err := r.q().ExecContext(ctx,
		`UPDATE movie_versions SET has_file = 1, file_path = ?, size_bytes = ?, media_json = ? WHERE id = ?`, path, size, mediaJSON, id)
	return err
}

// SetVersionMediaInfo caches an extra version's media info, but only while the track still
// holds the file at path: a background read must not stamp a file the track has since
// replaced with facts about the old one.
func (r *Repo) SetVersionMediaInfo(ctx context.Context, id int64, path, mediaJSON string) error {
	_, err := r.q().ExecContext(ctx,
		`UPDATE movie_versions SET media_json = ? WHERE id = ? AND has_file = 1 AND file_path = ?`, mediaJSON, id, path)
	return err
}

// ClearVersionFile marks an extra version as having no file.
func (r *Repo) ClearVersionFile(ctx context.Context, id int64) error {
	_, err := r.q().ExecContext(ctx, `UPDATE movie_versions SET has_file = 0, file_path = '', size_bytes = 0, source_release = '', upgrade_hold = 0,
		converted_from_release = '', converted_from_size = 0, media_json = '' WHERE id = ?`, id)
	return err
}

// holdChunk bounds how many ids go in one IN (...) list, well under SQLite's variable limit.
const holdChunk = 500

// HoldUpgrades sets the upgrade hold on the given movies' default files and on the given
// extra tracks, in one transaction, returning how many rows changed. Rows without a file are
// left alone: there's nothing to keep, and a missing file must still be searched for.
func (r *Repo) HoldUpgrades(ctx context.Context, movieIDs, versionIDs []int64) (heldMovies, heldVersions int, err error) {
	err = r.inTx(ctx, func(tx *sql.Tx, _ *Repo) error {
		var e error
		if heldMovies, e = holdRows(ctx, tx, "movies", movieIDs); e != nil {
			return e
		}
		heldVersions, e = holdRows(ctx, tx, "movie_versions", versionIDs)
		return e
	})
	if err != nil {
		return 0, 0, err
	}
	return heldMovies, heldVersions, nil
}

// holdRows sets upgrade_hold on rows of a fixed table (never user input) that have a file.
func holdRows(ctx context.Context, tx *sql.Tx, table string, ids []int64) (int, error) {
	n := 0
	for len(ids) > 0 {
		chunk := ids
		if len(chunk) > holdChunk {
			chunk = ids[:holdChunk]
		}
		ids = ids[len(chunk):]
		args := make([]any, len(chunk))
		for i, id := range chunk {
			args[i] = id
		}
		res, err := tx.ExecContext(ctx, `UPDATE `+table+` SET upgrade_hold = 1
			WHERE has_file = 1 AND upgrade_hold = 0 AND id IN (`+placeholders(len(chunk))+`)`, args...)
		if err != nil {
			return n, err
		}
		c, _ := res.RowsAffected()
		n += int(c)
	}
	return n, nil
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	b := make([]byte, 0, 2*n)
	for i := 0; i < n; i++ {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, '?')
	}
	return string(b)
}

// ResumeUpgrades clears the upgrade hold on a movie and every one of its tracks, returning
// how many were held.
func (r *Repo) ResumeUpgrades(ctx context.Context, movieID int64) (int, error) {
	n := 0
	err := r.inTx(ctx, func(tx *sql.Tx, _ *Repo) error {
		for _, q := range []string{
			`UPDATE movies SET upgrade_hold = 0 WHERE id = ? AND upgrade_hold = 1`,
			`UPDATE movie_versions SET upgrade_hold = 0 WHERE movie_id = ? AND upgrade_hold = 1`,
		} {
			res, err := tx.ExecContext(ctx, q, movieID)
			if err != nil {
				return err
			}
			c, _ := res.RowsAffected()
			n += int(c)
		}
		return nil
	})
	return n, err
}

// clearHold ends one track's upgrade hold: versionID 0 is the movie's default file.
func (r *Repo) clearHold(ctx context.Context, movieID, versionID int64) error {
	if versionID == 0 {
		_, err := r.q().ExecContext(ctx, `UPDATE movies SET upgrade_hold = 0 WHERE id = ?`, movieID)
		return err
	}
	_, err := r.q().ExecContext(ctx, `UPDATE movie_versions SET upgrade_hold = 0 WHERE id = ?`, versionID)
	return err
}

// DeleteVersionsForMovie removes all extra version tracks for a movie (used when
// deleting the movie).
func (r *Repo) DeleteVersionsForMovie(ctx context.Context, movieID int64) error {
	_, err := r.q().ExecContext(ctx, `DELETE FROM movie_versions WHERE movie_id = ?`, movieID)
	return err
}

// DeleteVersion removes an extra version track.
func (r *Repo) DeleteVersion(ctx context.Context, id int64) error {
	res, err := r.q().ExecContext(ctx, `DELETE FROM movie_versions WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Event is a single row in a movie's activity timeline.
type Event struct {
	Event     string `json:"event"`
	Detail    string `json:"detail,omitempty"`
	CreatedAt string `json:"created_at"`
}

// AddEvent appends a timeline event for a movie.
func (r *Repo) AddEvent(ctx context.Context, movieID int64, event, detail string) error {
	_, err := r.q().ExecContext(ctx,
		`INSERT INTO movie_events (movie_id, event, detail) VALUES (?, ?, ?)`, movieID, event, detail)
	return err
}

// Events returns a movie's timeline, newest first.
func (r *Repo) Events(ctx context.Context, movieID int64, limit int) ([]Event, error) {
	rows, err := r.q().QueryContext(ctx,
		`SELECT event, detail, created_at FROM movie_events WHERE movie_id = ? ORDER BY id DESC LIMIT ?`,
		movieID, limit)
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

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func isUnique(err error) bool {
	return err != nil && containsAny(err.Error(), "UNIQUE constraint failed")
}

func containsAny(s, sub string) bool {
	return len(s) >= len(sub) && (indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
