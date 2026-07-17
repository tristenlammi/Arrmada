package series

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

// ErrNotFound is returned when a series id doesn't exist.
var ErrNotFound = errors.New("series not found")

// ErrExists is returned when a TMDB series is already in the library.
var ErrExists = errors.New("series already in library")

// Repo persists series, seasons, and episodes in SQLite.
type Repo struct{ db *sql.DB }

// NewRepo builds a repository over the given pool.
func NewRepo(db *sql.DB) *Repo { return &Repo{db: db} }

const seriesCols = `id, tmdb_id, imdb_id, title, year, overview, poster_url, status, network,
	monitored, quality_profile, extra_json, series_type, added_at`

func scanSeries(row interface{ Scan(...any) error }) (Series, error) {
	var (
		s         Series
		mon       int
		extraJSON string
	)
	err := row.Scan(&s.ID, &s.TMDBID, &s.IMDBID, &s.Title, &s.Year, &s.Overview, &s.PosterURL,
		&s.Status, &s.Network, &mon, &s.QualityProfile, &extraJSON, &s.SeriesType, &s.AddedAt)
	if err != nil {
		return Series{}, err
	}
	s.Monitored = mon != 0
	if extraJSON != "" {
		var ex SeriesExtra
		if json.Unmarshal([]byte(extraJSON), &ex) == nil {
			s.Extra = &ex
		}
	}
	return s, nil
}

// List returns all series (newest first) with roll-up stats attached.
func (r *Repo) List(ctx context.Context) ([]Series, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+seriesCols+` FROM series ORDER BY added_at DESC, id DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Series
	for rows.Next() {
		s, err := scanSeries(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	stats, _ := r.allStats(ctx)
	for i := range out {
		if st, ok := stats[out[i].ID]; ok {
			out[i].Stats = st
		} else {
			out[i].Stats = &Stats{}
		}
	}
	return out, nil
}

// allStats returns per-series episode/file roll-ups keyed by series id.
func (r *Repo) allStats(ctx context.Context) (map[int64]*Stats, error) {
	out := map[int64]*Stats{}
	// Specials (season 0) are excluded from the have/total roll-up — a library isn't
	// "incomplete" just because an optional special hasn't been grabbed. The total also
	// only counts episodes that have already AIRED (or that we already have a file for),
	// so an in-progress season isn't marked incomplete for episodes that don't exist yet.
	rows, err := r.db.QueryContext(ctx,
		`SELECT series_id,
		        COALESCE(SUM(CASE WHEN has_file = 1 OR (air_date <> '' AND date(air_date) <= date('now')) THEN 1 ELSE 0 END), 0),
		        COALESCE(SUM(has_file),0),
		        COALESCE(SUM(size_bytes),0)
		 FROM episodes WHERE season_number > 0 GROUP BY series_id`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		st := &Stats{}
		if err := rows.Scan(&id, &st.Episodes, &st.HaveFiles, &st.SizeBytes); err != nil {
			return out, err
		}
		out[id] = st
	}
	sr, err := r.db.QueryContext(ctx, `SELECT series_id, COUNT(*) FROM seasons WHERE season_number > 0 GROUP BY series_id`)
	if err == nil {
		defer sr.Close()
		for sr.Next() {
			var id int64
			var n int
			if sr.Scan(&id, &n) == nil {
				if out[id] == nil {
					out[id] = &Stats{}
				}
				out[id].Seasons = n
			}
		}
	}
	return out, nil
}

// Get returns one series by id (no seasons/episodes attached).
func (r *Repo) Get(ctx context.Context, id int64) (Series, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+seriesCols+` FROM series WHERE id = ?`, id)
	s, err := scanSeries(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Series{}, ErrNotFound
	}
	return s, err
}

// Create inserts a series row.
func (r *Repo) Create(ctx context.Context, s Series) (Series, error) {
	extraJSON := ""
	if s.Extra != nil {
		if b, err := json.Marshal(s.Extra); err == nil {
			extraJSON = string(b)
		}
	}
	stype := s.SeriesType
	if stype == "" {
		stype = SeriesTypeStandard
	}
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO series (tmdb_id, imdb_id, title, year, overview, poster_url, status, network,
			monitored, quality_profile, extra_json, series_type)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.TMDBID, s.IMDBID, s.Title, s.Year, s.Overview, s.PosterURL, s.Status, s.Network,
		b2i(s.Monitored), s.QualityProfile, extraJSON, stype)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return Series{}, ErrExists
		}
		return Series{}, err
	}
	id, _ := res.LastInsertId()
	return r.Get(ctx, id)
}

// InsertSeasons inserts seasons and their episodes for a series.
func (r *Repo) InsertSeasons(ctx context.Context, seriesID int64, seasons []Season) error {
	for _, sn := range seasons {
		if _, err := r.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO seasons (series_id, season_number, name, overview, poster_url, monitored)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			seriesID, sn.SeasonNumber, sn.Name, sn.Overview, sn.PosterURL, b2i(sn.Monitored)); err != nil {
			return err
		}
		for _, ep := range sn.Episodes {
			if _, err := r.db.ExecContext(ctx,
				`INSERT OR IGNORE INTO episodes (series_id, season_number, episode_number, title, overview, air_date, runtime, still_url, monitored, absolute_number)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
				seriesID, ep.SeasonNumber, ep.EpisodeNumber, ep.Title, ep.Overview, ep.AirDate, ep.Runtime, ep.StillURL, b2i(ep.Monitored), ep.AbsoluteNumber); err != nil {
				return err
			}
		}
	}
	return nil
}

// SeasonsFor returns the seasons of a series (episodes attached), ordered.
func (r *Repo) SeasonsFor(ctx context.Context, seriesID int64) ([]Season, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, season_number, name, overview, poster_url, monitored FROM seasons WHERE series_id = ? ORDER BY season_number`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var seasons []Season
	byNum := map[int]int{} // season_number -> index in seasons
	for rows.Next() {
		var sn Season
		var mon int
		if err := rows.Scan(&sn.ID, &sn.SeasonNumber, &sn.Name, &sn.Overview, &sn.PosterURL, &mon); err != nil {
			return nil, err
		}
		sn.Monitored = mon != 0
		byNum[sn.SeasonNumber] = len(seasons)
		seasons = append(seasons, sn)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	eps, err := r.db.QueryContext(ctx,
		`SELECT id, season_number, episode_number, title, overview, air_date, runtime, still_url, monitored, has_file, file_path, size_bytes, absolute_number
		 FROM episodes WHERE series_id = ? ORDER BY season_number, episode_number`, seriesID)
	if err != nil {
		return seasons, nil
	}
	defer eps.Close()
	for eps.Next() {
		var e Episode
		var mon, hf int
		if err := eps.Scan(&e.ID, &e.SeasonNumber, &e.EpisodeNumber, &e.Title, &e.Overview, &e.AirDate, &e.Runtime, &e.StillURL, &mon, &hf, &e.FilePath, &e.SizeBytes, &e.AbsoluteNumber); err != nil {
			return seasons, nil
		}
		e.Monitored, e.HasFile = mon != 0, hf != 0
		if i, ok := byNum[e.SeasonNumber]; ok {
			seasons[i].Episodes = append(seasons[i].Episodes, e)
		}
	}
	return seasons, nil
}

// SetMonitored toggles a series' monitored flag.
func (r *Repo) SetMonitored(ctx context.Context, id int64, monitored bool) error {
	_, err := r.db.ExecContext(ctx, `UPDATE series SET monitored = ? WHERE id = ?`, b2i(monitored), id)
	return err
}

// SetSeriesType sets a series' numbering type ("standard" | "anime").
func (r *Repo) SetSeriesType(ctx context.Context, id int64, seriesType string) error {
	res, err := r.db.ExecContext(ctx, `UPDATE series SET series_type = ? WHERE id = ?`, seriesType, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetEpisodeAbsolute records an episode's absolute number (backfill on refresh).
func (r *Repo) SetEpisodeAbsolute(ctx context.Context, seriesID int64, season, episode, absolute int) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE episodes SET absolute_number = ? WHERE series_id = ? AND season_number = ? AND episode_number = ?`,
		absolute, seriesID, season, episode)
	return err
}

// BackfillAbsolute (re)computes every episode's absolute number as its 1-based
// ordinal across the non-special seasons (ordered by season then episode). Idempotent
// — safe to run on refresh, and it retro-fits series added before absolute numbering.
func (r *Repo) BackfillAbsolute(ctx context.Context, seriesID int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE episodes SET absolute_number = (
			SELECT COUNT(*) FROM episodes e2
			WHERE e2.series_id = episodes.series_id AND e2.season_number > 0
			  AND (e2.season_number < episodes.season_number
			       OR (e2.season_number = episodes.season_number AND e2.episode_number <= episodes.episode_number))
		) WHERE series_id = ? AND season_number > 0`, seriesID)
	return err
}

// EpisodeTitle returns an episode's title (empty when unknown).
func (r *Repo) EpisodeTitle(ctx context.Context, seriesID int64, season, episode int) string {
	var title string
	_ = r.db.QueryRowContext(ctx,
		`SELECT title FROM episodes WHERE series_id = ? AND season_number = ? AND episode_number = ? LIMIT 1`,
		seriesID, season, episode).Scan(&title)
	return title
}

// EpisodeExists reports whether a series has an episode with that (season, number).
func (r *Repo) EpisodeExists(ctx context.Context, seriesID int64, season, episode int) bool {
	var one int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM episodes WHERE series_id = ? AND season_number = ? AND episode_number = ? LIMIT 1`,
		seriesID, season, episode).Scan(&one)
	return err == nil && one == 1
}

// EpisodeByAbsolute resolves an absolute episode number to its (season, episode).
// ok=false when the series has no episode with that absolute number.
func (r *Repo) EpisodeByAbsolute(ctx context.Context, seriesID int64, absolute int) (season, episode int, ok bool) {
	err := r.db.QueryRowContext(ctx,
		`SELECT season_number, episode_number FROM episodes WHERE series_id = ? AND absolute_number = ? LIMIT 1`,
		seriesID, absolute).Scan(&season, &episode)
	if err != nil {
		return 0, 0, false
	}
	return season, episode, true
}

// NthEpisodeOfSeason resolves the n-th (1-based) aired episode of a season to its
// episode number — the positional fallback for anime files numbered per cour
// ("S03E01" → the first episode of season 3). ok=false when out of range.
func (r *Repo) NthEpisodeOfSeason(ctx context.Context, seriesID int64, season, n int) (episode int, ok bool) {
	if n < 1 {
		return 0, false
	}
	err := r.db.QueryRowContext(ctx,
		`SELECT episode_number FROM episodes WHERE series_id = ? AND season_number = ?
		 ORDER BY episode_number LIMIT 1 OFFSET ?`,
		seriesID, season, n-1).Scan(&episode)
	if err != nil {
		return 0, false
	}
	return episode, true
}

// SetSeasonMonitored toggles a whole season (and its episodes).
func (r *Repo) SetSeasonMonitored(ctx context.Context, seriesID, seasonNumber int64, monitored bool) error {
	if _, err := r.db.ExecContext(ctx, `UPDATE seasons SET monitored = ? WHERE series_id = ? AND season_number = ?`, b2i(monitored), seriesID, seasonNumber); err != nil {
		return err
	}
	_, err := r.db.ExecContext(ctx, `UPDATE episodes SET monitored = ? WHERE series_id = ? AND season_number = ?`, b2i(monitored), seriesID, seasonNumber)
	return err
}

// SetEpisodeMonitored toggles a single episode.
func (r *Repo) SetEpisodeMonitored(ctx context.Context, episodeID int64, monitored bool) error {
	res, err := r.db.ExecContext(ctx, `UPDATE episodes SET monitored = ? WHERE id = ?`, b2i(monitored), episodeID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SetEpisodeFile records that an episode now has a file on disk.
func (r *Repo) SetEpisodeFile(ctx context.Context, seriesID int64, season, episode int, path string, size int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE episodes SET has_file = 1, file_path = ?, size_bytes = ? WHERE series_id = ? AND season_number = ? AND episode_number = ?`,
		path, size, seriesID, season, episode)
	return err
}

// ClearEpisodeFile flips an episode back to wanted (no file), e.g. after deleting its file.
func (r *Repo) ClearEpisodeFile(ctx context.Context, seriesID int64, season, episode int) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE episodes SET has_file = 0, file_path = '', size_bytes = 0 WHERE series_id = ? AND season_number = ? AND episode_number = ?`,
		seriesID, season, episode)
	return err
}

// EpisodeFilePath returns the on-disk path of one episode's file (empty if none).
func (r *Repo) EpisodeFilePath(ctx context.Context, seriesID int64, season, episode int) (string, error) {
	var path string
	err := r.db.QueryRowContext(ctx,
		`SELECT file_path FROM episodes WHERE series_id = ? AND season_number = ? AND episode_number = ? AND has_file = 1`,
		seriesID, season, episode).Scan(&path)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return path, err
}

// AnyEpisodeFilePath returns the on-disk path of any one episode with a file for
// the series (empty if the series has nothing on disk). Used to discover the
// show's existing library folder so new episodes join it.
func (r *Repo) AnyEpisodeFilePath(ctx context.Context, seriesID int64) (string, error) {
	var path string
	err := r.db.QueryRowContext(ctx,
		`SELECT file_path FROM episodes WHERE series_id = ? AND has_file = 1 AND file_path <> '' LIMIT 1`,
		seriesID).Scan(&path)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return path, err
}

// SetQualityProfile changes a series' quality profile.
func (r *Repo) SetQualityProfile(ctx context.Context, id int64, profile string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE series SET quality_profile = ? WHERE id = ?`, profile, id)
	return err
}

// Delete removes a series and (via cascade) its seasons/episodes.
func (r *Repo) Delete(ctx context.Context, id int64) error {
	res, err := r.db.ExecContext(ctx, `DELETE FROM series WHERE id = ?`, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// Event is one entry in a series' activity timeline.
type Event struct {
	Event     string `json:"event"`
	Detail    string `json:"detail,omitempty"`
	CreatedAt string `json:"created_at"`
}

// AddEvent appends a timeline event for a series (best effort).
func (r *Repo) AddEvent(ctx context.Context, seriesID int64, event, detail string) {
	_, _ = r.db.ExecContext(ctx,
		`INSERT INTO series_events (series_id, event, detail) VALUES (?, ?, ?)`, seriesID, event, detail)
}

// Events returns a series' timeline, newest first.
func (r *Repo) Events(ctx context.Context, seriesID int64, limit int) ([]Event, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT event, detail, created_at FROM series_events WHERE series_id = ? ORDER BY id DESC LIMIT ?`,
		seriesID, limit)
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

func b2i(b bool) int {
	if b {
		return 1
	}
	return 0
}
