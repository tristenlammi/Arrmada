package movies

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
)

// ErrNotFound is returned when a movie id doesn't exist.
var ErrNotFound = errors.New("movie not found")

// ErrExists is returned when a TMDB movie is already in the library.
var ErrExists = errors.New("movie already in library")

// Repo persists movies in SQLite.
type Repo struct{ db *sql.DB }

// NewRepo builds a repository over the given pool.
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

const movieCols = `id, tmdb_id, imdb_id, title, year, overview, poster_url, runtime, status,
	monitored, quality_profile, min_availability, has_file, movie_file_path, added_at, extra_json, media_json,
	source_release`

func (r *Repo) scan(row interface{ Scan(...any) error }) (Movie, error) {
	var (
		m                    Movie
		mon, hf              int
		extraJSON, mediaJSON string
	)
	err := row.Scan(&m.ID, &m.TMDBID, &m.IMDBID, &m.Title, &m.Year, &m.Overview, &m.PosterURL,
		&m.Runtime, &m.Status, &mon, &m.QualityProfile, &m.MinAvailability, &hf, &m.MovieFilePath,
		&m.AddedAt, &extraJSON, &mediaJSON, &m.SourceRelease)
	if err != nil {
		return Movie{}, err
	}
	m.Monitored = mon != 0
	m.HasFile = hf != 0
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
	_, err := r.db.ExecContext(ctx, `UPDATE movies SET media_json = ? WHERE id = ?`, mediaJSON, id)
	return err
}

// List returns all movies, newest first.
func (r *Repo) List(ctx context.Context) ([]Movie, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+movieCols+` FROM movies ORDER BY added_at DESC, id DESC`)
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
	rows, err := r.db.QueryContext(ctx, `SELECT tmdb_id FROM movies`)
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

// MonitoredMissing returns monitored movies with no file (the search targets).
func (r *Repo) MonitoredMissing(ctx context.Context) ([]Movie, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+movieCols+` FROM movies WHERE monitored = 1 AND has_file = 0`)
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
	row := r.db.QueryRowContext(ctx, `SELECT `+movieCols+` FROM movies WHERE id = ?`, id)
	m, err := r.scan(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Movie{}, ErrNotFound
	}
	return m, err
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
	res, err := r.db.ExecContext(ctx,
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

// Delete removes a movie by id.
func (r *Repo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM movies WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetMonitored toggles monitoring.
func (r *Repo) SetMonitored(ctx context.Context, id int64, monitored bool) error {
	_, err := r.db.ExecContext(ctx, `UPDATE movies SET monitored = ? WHERE id = ?`, boolToInt(monitored), id)
	return err
}

// SearchState returns when the movie was last swept and how many consecutive sweeps
// grabbed nothing (drives the search backoff).
func (r *Repo) SearchState(ctx context.Context, movieID int64) (lastSearchAt string, misses int) {
	_ = r.db.QueryRowContext(ctx,
		`SELECT last_search_at, search_misses FROM movies WHERE id = ?`, movieID).Scan(&lastSearchAt, &misses)
	return lastSearchAt, misses
}

// RecordSearchMiss stamps the sweep time and increments the miss counter.
func (r *Repo) RecordSearchMiss(ctx context.Context, movieID int64) {
	_, _ = r.db.ExecContext(ctx,
		`UPDATE movies SET last_search_at = datetime('now'), search_misses = search_misses + 1 WHERE id = ?`, movieID)
}

// ResetSearchMisses clears the backoff after a successful grab.
func (r *Repo) ResetSearchMisses(ctx context.Context, movieID int64) {
	_, _ = r.db.ExecContext(ctx,
		`UPDATE movies SET last_search_at = datetime('now'), search_misses = 0 WHERE id = ?`, movieID)
}

// SetFile marks a movie as having a file at path.
func (r *Repo) SetFile(ctx context.Context, id int64, path string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE movies SET has_file = 1, movie_file_path = ? WHERE id = ?`, path, id)
	return err
}

// ClearFile marks a movie as having no file (after its file is deleted).
func (r *Repo) ClearFile(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE movies SET has_file = 0, movie_file_path = '', media_json = '', source_release = '' WHERE id = ?`, id)
	return err
}

// SetSourceRelease records the release name the default file was imported from
// (used to score the current file when deciding upgrades).
func (r *Repo) SetSourceRelease(ctx context.Context, id int64, release string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE movies SET source_release = ? WHERE id = ?`, release, id)
	return err
}

// SetVersionSourceRelease records the release name an extra version's file came from.
func (r *Repo) SetVersionSourceRelease(ctx context.Context, id int64, release string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE movie_versions SET source_release = ? WHERE id = ?`, release, id)
	return err
}

// SetQualityProfile changes a movie's quality profile.
func (r *Repo) SetQualityProfile(ctx context.Context, id int64, profile string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE movies SET quality_profile = ? WHERE id = ?`, profile, id)
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
	res, err := r.db.ExecContext(ctx, `UPDATE movies SET min_availability = ? WHERE id = ?`, avail, id)
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
	_, err := r.db.ExecContext(ctx,
		`UPDATE movies SET imdb_id = ?, title = ?, year = ?, overview = ?, poster_url = ?,
			runtime = ?, status = ?, extra_json = ? WHERE id = ?`,
		m.IMDBID, m.Title, m.Year, m.Overview, m.PosterURL, m.Runtime, m.Status, extraJSON, id)
	return err
}

// --- extra version tracks -------------------------------------------------

const versionCols = `id, movie_id, label, quality_profile, edition, monitored, has_file, file_path, size_bytes, source_release`

func scanVersion(row interface{ Scan(...any) error }) (Version, int64, error) {
	var (
		v       Version
		movieID int64
		mon, hf int
	)
	err := row.Scan(&v.ID, &movieID, &v.Label, &v.QualityProfile, &v.Edition, &mon, &hf, &v.FilePath, &v.SizeBytes, &v.SourceRelease)
	if err != nil {
		return Version{}, 0, err
	}
	v.Monitored = mon != 0
	v.HasFile = hf != 0
	return v, movieID, err
}

// ListVersions returns the extra version tracks for a movie.
func (r *Repo) ListVersions(ctx context.Context, movieID int64) ([]Version, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+versionCols+` FROM movie_versions WHERE movie_id = ? ORDER BY id`, movieID)
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

// GetVersion returns one extra version plus its movie id.
func (r *Repo) GetVersion(ctx context.Context, id int64) (Version, int64, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+versionCols+` FROM movie_versions WHERE id = ?`, id)
	v, movieID, err := scanVersion(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, 0, ErrNotFound
	}
	return v, movieID, err
}

// CreateVersion adds an extra version track.
func (r *Repo) CreateVersion(ctx context.Context, movieID int64, v Version) (Version, error) {
	res, err := r.db.ExecContext(ctx,
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
	res, err := r.db.ExecContext(ctx,
		`UPDATE movie_versions SET label = ?, quality_profile = ?, edition = ?, monitored = ? WHERE id = ?`,
		label, profile, edition, boolToInt(monitored), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetVersionFile records a file for an extra version.
func (r *Repo) SetVersionFile(ctx context.Context, id int64, path string, size int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE movie_versions SET has_file = 1, file_path = ?, size_bytes = ? WHERE id = ?`, path, size, id)
	return err
}

// ClearVersionFile marks an extra version as having no file.
func (r *Repo) ClearVersionFile(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE movie_versions SET has_file = 0, file_path = '', size_bytes = 0, source_release = '' WHERE id = ?`, id)
	return err
}

// DeleteVersionsForMovie removes all extra version tracks for a movie (used when
// deleting the movie).
func (r *Repo) DeleteVersionsForMovie(ctx context.Context, movieID int64) error {
	_, err := r.db.ExecContext(ctx, `DELETE FROM movie_versions WHERE movie_id = ?`, movieID)
	return err
}

// DeleteVersion removes an extra version track.
func (r *Repo) DeleteVersion(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM movie_versions WHERE id = ?`, id)
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
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO movie_events (movie_id, event, detail) VALUES (?, ?, ?)`, movieID, event, detail)
	return err
}

// Events returns a movie's timeline, newest first.
func (r *Repo) Events(ctx context.Context, movieID int64, limit int) ([]Event, error) {
	rows, err := r.db.QueryContext(ctx,
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
