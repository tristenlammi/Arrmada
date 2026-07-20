package series

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
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
	monitored, quality_profile, extra_json, series_type, tvdb_id, added_at`

func scanSeries(row interface{ Scan(...any) error }) (Series, error) {
	var (
		s         Series
		mon       int
		extraJSON string
	)
	err := row.Scan(&s.ID, &s.TMDBID, &s.IMDBID, &s.Title, &s.Year, &s.Overview, &s.PosterURL,
		&s.Status, &s.Network, &mon, &s.QualityProfile, &extraJSON, &s.SeriesType, &s.TVDBID, &s.AddedAt)
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
			monitored, quality_profile, extra_json, series_type, tvdb_id)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.TMDBID, s.IMDBID, s.Title, s.Year, s.Overview, s.PosterURL, s.Status, s.Network,
		b2i(s.Monitored), s.QualityProfile, extraJSON, stype, s.TVDBID)
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
		`SELECT id, season_number, episode_number, title, overview, air_date, runtime, still_url, monitored, has_file, file_path, size_bytes, absolute_number, source_release
		 FROM episodes WHERE series_id = ? ORDER BY season_number, episode_number`, seriesID)
	if err != nil {
		return seasons, nil
	}
	defer eps.Close()
	for eps.Next() {
		var e Episode
		var mon, hf int
		if err := eps.Scan(&e.ID, &e.SeasonNumber, &e.EpisodeNumber, &e.Title, &e.Overview, &e.AirDate, &e.Runtime, &e.StillURL, &mon, &hf, &e.FilePath, &e.SizeBytes, &e.AbsoluteNumber, &e.SourceRelease); err != nil {
			return seasons, nil
		}
		e.Monitored, e.HasFile = mon != 0, hf != 0
		if i, ok := byNum[e.SeasonNumber]; ok {
			seasons[i].Episodes = append(seasons[i].Episodes, e)
		}
	}
	return seasons, nil
}

// SetMonitored toggles a series' monitored flag AND cascades it to the show's seasons
// and episodes.
//
// Without the cascade, flipping a show to monitored only updated the series row while
// every episode stayed unmonitored — and the search only ever grabs episodes where
// monitored = 1. A show would read "Monitored" in the UI and silently never grab
// anything, which is especially misleading when monitoring shows in bulk.
//
// Enabling deliberately skips specials (season 0), matching how a series is added
// (seasonsFromDetails monitors `monitored && !special`). Disabling covers everything —
// nothing should be grabbed for a show you've switched off.
func (r *Repo) SetMonitored(ctx context.Context, id int64, monitored bool) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }() // no-op once committed

	if _, err := tx.ExecContext(ctx, `UPDATE series SET monitored = ? WHERE id = ?`, b2i(monitored), id); err != nil {
		return err
	}
	// season_number > 0 leaves specials alone when enabling; when disabling we want
	// everything off, so the filter is dropped.
	scope := ` AND season_number > 0`
	if !monitored {
		scope = ``
	}
	if _, err := tx.ExecContext(ctx, `UPDATE seasons SET monitored = ? WHERE series_id = ?`+scope, b2i(monitored), id); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE episodes SET monitored = ? WHERE series_id = ?`+scope, b2i(monitored), id); err != nil {
		return err
	}
	return tx.Commit()
}

// SetTVDBID records a series' TVDB id (the TheXEM lookup key).
func (r *Repo) SetTVDBID(ctx context.Context, id int64, tvdbID int) error {
	_, err := r.db.ExecContext(ctx, `UPDATE series SET tvdb_id = ? WHERE id = ?`, tvdbID, id)
	return err
}

// SetSceneMap caches a series' fetched scene→absolute map (JSON) with a fetch timestamp.
func (r *Repo) SetSceneMap(ctx context.Context, id int64, sceneJSON string, fetchedAt int64) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO series_scene_map (series_id, scene_json, fetched_at) VALUES (?, ?, ?)
		 ON CONFLICT(series_id) DO UPDATE SET scene_json = excluded.scene_json, fetched_at = excluded.fetched_at`,
		id, sceneJSON, fetchedAt)
	return err
}

// SceneMap returns a series' cached scene→absolute JSON ("" when none cached).
func (r *Repo) SceneMap(ctx context.Context, id int64) string {
	var j string
	_ = r.db.QueryRowContext(ctx, `SELECT scene_json FROM series_scene_map WHERE series_id = ?`, id).Scan(&j)
	return j
}

// SceneOverride is a manual "scene season N starts at TMDB SxxEyy" mapping.
type SceneOverride struct {
	SceneSeason int `json:"scene_season"`
	TMDBSeason  int `json:"tmdb_season"`
	TMDBEpisode int `json:"tmdb_episode"`
}

// SceneOverrides returns a series' manual scene-season mappings, lowest scene season first.
func (r *Repo) SceneOverrides(ctx context.Context, seriesID int64) []SceneOverride {
	rows, err := r.db.QueryContext(ctx,
		`SELECT scene_season, tmdb_season, tmdb_episode FROM series_scene_overrides
		 WHERE series_id = ? ORDER BY scene_season`, seriesID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []SceneOverride
	for rows.Next() {
		var o SceneOverride
		if rows.Scan(&o.SceneSeason, &o.TMDBSeason, &o.TMDBEpisode) == nil {
			out = append(out, o)
		}
	}
	return out
}

// SceneOverrideFor returns the mapping for one scene season, if the user set one.
func (r *Repo) SceneOverrideFor(ctx context.Context, seriesID int64, sceneSeason int) (SceneOverride, bool) {
	o := SceneOverride{SceneSeason: sceneSeason}
	err := r.db.QueryRowContext(ctx,
		`SELECT tmdb_season, tmdb_episode FROM series_scene_overrides
		 WHERE series_id = ? AND scene_season = ?`, seriesID, sceneSeason).Scan(&o.TMDBSeason, &o.TMDBEpisode)
	return o, err == nil
}

// SetSceneOverride records (or replaces) one scene-season mapping.
func (r *Repo) SetSceneOverride(ctx context.Context, seriesID int64, o SceneOverride) error {
	_, err := r.db.ExecContext(ctx,
		`INSERT INTO series_scene_overrides (series_id, scene_season, tmdb_season, tmdb_episode)
		 VALUES (?, ?, ?, ?)
		 ON CONFLICT(series_id, scene_season) DO UPDATE SET
		   tmdb_season = excluded.tmdb_season, tmdb_episode = excluded.tmdb_episode`,
		seriesID, o.SceneSeason, o.TMDBSeason, o.TMDBEpisode)
	return err
}

// DeleteSceneOverride drops one scene-season mapping.
func (r *Repo) DeleteSceneOverride(ctx context.Context, seriesID int64, sceneSeason int) error {
	_, err := r.db.ExecContext(ctx,
		`DELETE FROM series_scene_overrides WHERE series_id = ? AND scene_season = ?`, seriesID, sceneSeason)
	return err
}

// AbsoluteOf returns an episode's absolute number (0 when unknown) — used to walk a
// scene cour forward from its mapped starting episode, even across a season boundary.
func (r *Repo) AbsoluteOf(ctx context.Context, seriesID int64, season, episode int) int {
	var abs int
	_ = r.db.QueryRowContext(ctx,
		`SELECT absolute_number FROM episodes WHERE series_id = ? AND season_number = ? AND episode_number = ?`,
		seriesID, season, episode).Scan(&abs)
	return abs
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

// SeasonExists reports whether a series has any episode in the given season.
func (r *Repo) SeasonExists(ctx context.Context, seriesID int64, season int) bool {
	var one int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM episodes WHERE series_id = ? AND season_number = ? LIMIT 1`,
		seriesID, season).Scan(&one)
	return err == nil && one == 1
}

// SeasonHasMissing reports whether a season still has an aired episode with no file on
// disk — i.e. a re-processed pack could still fill something. Monitoring is deliberately
// NOT considered: if the file is already downloaded, it should import regardless of
// whether Arrmada would auto-grab the episode. Unaired episodes don't count (they can't
// be filled yet), so an ongoing show doesn't look perpetually incomplete. season <= 0
// checks the whole series.
func (r *Repo) SeasonHasMissing(ctx context.Context, seriesID int64, season int) bool {
	q := `SELECT 1 FROM episodes
	      WHERE series_id = ? AND has_file = 0 AND season_number > 0
	        AND air_date != '' AND air_date <= date('now')`
	args := []any{seriesID}
	if season > 0 {
		q += ` AND season_number = ?`
		args = append(args, season)
	}
	q += ` LIMIT 1`
	var one int
	err := r.db.QueryRowContext(ctx, q, args...).Scan(&one)
	return err == nil && one == 1
}

// SearchState returns when the series was last swept and how many consecutive sweeps
// found nothing to grab (drives the search backoff).
func (r *Repo) SearchState(ctx context.Context, seriesID int64) (lastSearchAt string, misses int) {
	_ = r.db.QueryRowContext(ctx,
		`SELECT last_search_at, search_misses FROM series WHERE id = ?`, seriesID).Scan(&lastSearchAt, &misses)
	return lastSearchAt, misses
}

// RecordSearchMiss stamps the sweep time and increments the miss counter.
func (r *Repo) RecordSearchMiss(ctx context.Context, seriesID int64) {
	_, _ = r.db.ExecContext(ctx,
		`UPDATE series SET last_search_at = datetime('now'), search_misses = search_misses + 1 WHERE id = ?`, seriesID)
}

// ResetSearchMisses clears the backoff after a successful grab.
func (r *Repo) ResetSearchMisses(ctx context.Context, seriesID int64) {
	_, _ = r.db.ExecContext(ctx,
		`UPDATE series SET last_search_at = datetime('now'), search_misses = 0 WHERE id = ?`, seriesID)
}

// HasWantedEpisodes reports whether a series has an episode the automation would
// actually grab: monitored, aired, and with no file. Mirrors wantedEpisodes' filter so
// the missing-sweep can skip a series without spending an indexer search on it.
//
// An episode with no air date is UNAIRED and is not wanted — it's almost always a TMDB
// placeholder padding out a season, and treating it as wanted made the searcher hunt
// forever for episodes that don't exist. SeasonHasMissing and automation's aired() apply
// the same rule; when these disagreed, a fully-imported show re-grabbed its own pack on
// every sweep with nothing able to stop it.
func (r *Repo) HasWantedEpisodes(ctx context.Context, seriesID int64) bool {
	var one int
	err := r.db.QueryRowContext(ctx,
		`SELECT 1 FROM episodes
		 WHERE series_id = ? AND monitored = 1 AND has_file = 0 AND season_number > 0
		   AND air_date != '' AND air_date <= date('now')
		 LIMIT 1`, seriesID).Scan(&one)
	return err == nil && one == 1
}

// SeriesAcquisition summarizes a monitored series' outstanding episodes for the
// downloads feed: how many aired episodes are still wanted (being searched) and the
// soonest monitored episode that hasn't aired yet (upcoming).
type SeriesAcquisition struct {
	ID             int64
	Title          string
	Year           int
	PosterURL      string
	QualityProfile string
	SearchingCount int    // aired, monitored, missing episodes
	NextAir        string // soonest future monitored+missing episode air date (YYYY-MM-DD), "" if none
	NextLabel      string // "S02E13" for the upcoming episode, "" if none
}

// AcquisitionSummary returns per-monitored-series counts of wanted (aired, missing)
// episodes and the next upcoming episode, in one pass. Only series with something
// outstanding in either bucket are worth returning; the caller filters.
func (r *Repo) AcquisitionSummary(ctx context.Context) ([]SeriesAcquisition, error) {
	rows, err := r.db.QueryContext(ctx, `
		SELECT s.id, s.title, s.year, s.poster_url, s.quality_profile,
		  SUM(CASE WHEN e.monitored = 1 AND e.has_file = 0 AND e.season_number > 0
		           AND e.air_date != '' AND e.air_date <= date('now') THEN 1 ELSE 0 END) AS searching,
		  MIN(CASE WHEN e.monitored = 1 AND e.has_file = 0 AND e.air_date > date('now')
		           THEN e.air_date END) AS next_air
		FROM series s
		JOIN episodes e ON e.series_id = s.id
		WHERE s.monitored = 1
		GROUP BY s.id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []SeriesAcquisition
	for rows.Next() {
		var a SeriesAcquisition
		var nextAir sql.NullString
		if err := rows.Scan(&a.ID, &a.Title, &a.Year, &a.PosterURL, &a.QualityProfile, &a.SearchingCount, &nextAir); err != nil {
			return nil, err
		}
		if nextAir.Valid {
			a.NextAir = nextAir.String
			if s, e, ok := r.episodeAtAir(ctx, a.ID, nextAir.String); ok {
				a.NextLabel = fmtSxxExx(s, e)
			}
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// episodeAtAir returns the (season, episode) of the monitored, missing episode airing
// on the given date — the label for an upcoming row.
func (r *Repo) episodeAtAir(ctx context.Context, seriesID int64, air string) (season, episode int, ok bool) {
	err := r.db.QueryRowContext(ctx,
		`SELECT season_number, episode_number FROM episodes
		 WHERE series_id = ? AND air_date = ? AND monitored = 1 AND has_file = 0
		 ORDER BY season_number, episode_number LIMIT 1`, seriesID, air).Scan(&season, &episode)
	return season, episode, err == nil
}

func fmtSxxExx(s, e int) string { return fmt.Sprintf("S%02dE%02d", s, e) }

// epAir is one episode's (season, episode) with its air date, for scene-season inference.
type epAir struct {
	season, episode int
	airDate         string
}

// OrderedEpisodes returns a series' non-special episodes in absolute (season, episode)
// order with their air dates — the input to air-date-gap scene-season inference.
func (r *Repo) OrderedEpisodes(ctx context.Context, seriesID int64) []epAir {
	rows, err := r.db.QueryContext(ctx,
		`SELECT season_number, episode_number, air_date FROM episodes
		 WHERE series_id = ? AND season_number > 0 ORDER BY season_number, episode_number`, seriesID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []epAir
	for rows.Next() {
		var e epAir
		if rows.Scan(&e.season, &e.episode, &e.airDate) == nil {
			out = append(out, e)
		}
	}
	return out
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

// RepointEpisodeFile moves EVERY episode currently pointing at oldPath to newPath.
//
// A single file can serve several episodes — a double-length "S03E01E02" is one file with
// two episode rows. Updating just one of them leaves the others pointing at a path that
// may no longer exist (a convert can change the container), so the reverse lookup has to
// be by path, not by episode number.
func (r *Repo) RepointEpisodeFile(ctx context.Context, seriesID int64, oldPath, newPath string, size int64) (int64, error) {
	res, err := r.db.ExecContext(ctx,
		`UPDATE episodes SET has_file = 1, file_path = ?, size_bytes = ?
		  WHERE series_id = ? AND file_path = ?`,
		newPath, size, seriesID, oldPath)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return n, nil
}

// SetEpisodeSourceRelease records the release name an episode's file was imported from.
// Kept separate from SetEpisodeFile so path-only updates (rename, transcode) preserve it.
func (r *Repo) SetEpisodeSourceRelease(ctx context.Context, seriesID int64, season, episode int, release string) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE episodes SET source_release = ? WHERE series_id = ? AND season_number = ? AND episode_number = ?`,
		release, seriesID, season, episode)
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
// EpisodeFile describes the file an episode currently has, for upgrade decisions.
// Path is "" when the episode has no file.
type EpisodeFile struct {
	Path          string
	SizeBytes     int64
	SourceRelease string // the release it came from, not the renamed library file
	RuntimeMin    int    // needed to turn size into a bitrate
}

// CurrentEpisodeFile returns what an episode currently holds, so an import can be judged
// against it on more than resolution alone.
func (r *Repo) CurrentEpisodeFile(ctx context.Context, seriesID int64, season, episode int) EpisodeFile {
	var f EpisodeFile
	err := r.db.QueryRowContext(ctx,
		`SELECT file_path, size_bytes, source_release, runtime FROM episodes
		 WHERE series_id = ? AND season_number = ? AND episode_number = ? AND has_file = 1`,
		seriesID, season, episode).Scan(&f.Path, &f.SizeBytes, &f.SourceRelease, &f.RuntimeMin)
	if err != nil {
		return EpisodeFile{}
	}
	return f
}

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
// FolderSharedWith returns the ids of OTHER series that also store episodes in the given
// library folder name.
//
// Two shows in one folder is corruption waiting to happen: their season directories merge,
// and any episode number they share collides. "Teen Titans" and "Teen Titans Go!" are the
// obvious pair, but any show whose folder was renamed to another's name does it.
func (r *Repo) FolderSharedWith(ctx context.Context, seriesID int64, folder string) []int64 {
	if folder == "" {
		return nil
	}
	// Match the folder as a whole path segment, so "Teen Titans" doesn't match
	// "Teen Titans Go".
	rows, err := r.db.QueryContext(ctx,
		`SELECT DISTINCT series_id FROM episodes
		 WHERE series_id != ? AND has_file = 1 AND file_path LIKE '%/' || ? || '/%'`,
		seriesID, folder)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			out = append(out, id)
		}
	}
	return out
}

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
