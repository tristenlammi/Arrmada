package series

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/tristenlammi/arrmada/internal/store"
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
	monitored, quality_profile, extra_json, series_type, tvdb_id, added_at, numbering_source,
	last_refreshed_at, monitor_new_seasons`

func scanSeries(row interface{ Scan(...any) error }) (Series, error) {
	var (
		s         Series
		mon, mns  int
		extraJSON string
	)
	err := row.Scan(&s.ID, &s.TMDBID, &s.IMDBID, &s.Title, &s.Year, &s.Overview, &s.PosterURL,
		&s.Status, &s.Network, &mon, &s.QualityProfile, &extraJSON, &s.SeriesType, &s.TVDBID, &s.AddedAt, &s.NumberingSource,
		&s.LastRefreshedAt, &mns)
	if err != nil {
		return Series{}, err
	}
	s.Monitored, s.MonitorNewSeasons = mon != 0, mns != 0
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
			monitored, quality_profile, extra_json, series_type, tvdb_id, monitor_new_seasons)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		s.TMDBID, s.IMDBID, s.Title, s.Year, s.Overview, s.PosterURL, s.Status, s.Network,
		b2i(s.Monitored), s.QualityProfile, extraJSON, stype, s.TVDBID, b2i(s.MonitorNewSeasons))
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
			// INSERT OR IGNORE alone froze an episode's metadata at whatever it was when
			// the show was added: a refresh could never correct a title, and — since
			// dateless episodes are treated as unaired — could never learn an air date
			// TMDB published later, leaving those episodes unsearchable forever.
			//
			// Only the metadata is refreshed. Monitoring, file state, size and the
			// recorded source release are the user's and the library's, not TMDB's.
			if _, err := r.db.ExecContext(ctx,
				`UPDATE episodes SET title = ?, overview = ?, air_date = ?, runtime = ?, still_url = ?
				 WHERE series_id = ? AND season_number = ? AND episode_number = ?`,
				ep.Title, ep.Overview, ep.AirDate, ep.Runtime, ep.StillURL,
				seriesID, ep.SeasonNumber, ep.EpisodeNumber); err != nil {
				return err
			}
		}
	}
	return nil
}

// SetNumberingSource records whose listing the stored episode numbering now follows.
func (r *Repo) SetNumberingSource(ctx context.Context, seriesID int64, source string) error {
	_, err := r.db.ExecContext(ctx, `UPDATE series SET numbering_source = ? WHERE id = ?`, source, seriesID)
	return err
}

// InsertNewEpisodes adds only the (season, episode) rows the series doesn't have yet, and
// leaves every existing row exactly as it is — title, air date and absolute number
// included. It's what a refresh can safely do with a listing it doesn't trust to number
// the show (a stand-in after a source failed, or one the stored numbering disagrees with):
// that listing may number episodes differently, so writing its metadata onto existing rows
// by (season, episode) would put one episode's title and date on another.
//
// New rows get no absolute number of their own; BackfillAbsolute counts one in, so a
// stand-in's absolutes can't collide with the stored ones.
func (r *Repo) InsertNewEpisodes(ctx context.Context, seriesID int64, seasons []Season) (int, error) {
	added := 0
	for _, sn := range seasons {
		if _, err := r.db.ExecContext(ctx,
			`INSERT OR IGNORE INTO seasons (series_id, season_number, name, overview, poster_url, monitored)
			 VALUES (?, ?, ?, ?, ?, ?)`,
			seriesID, sn.SeasonNumber, sn.Name, sn.Overview, sn.PosterURL, b2i(sn.Monitored)); err != nil {
			return added, err
		}
		for _, ep := range sn.Episodes {
			res, err := r.db.ExecContext(ctx,
				`INSERT OR IGNORE INTO episodes (series_id, season_number, episode_number, title, overview, air_date, runtime, still_url, monitored, absolute_number)
				 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, 0)`,
				seriesID, ep.SeasonNumber, ep.EpisodeNumber, ep.Title, ep.Overview, ep.AirDate, ep.Runtime, ep.StillURL, b2i(ep.Monitored))
			if err != nil {
				return added, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				added++
			}
		}
	}
	return added, nil
}

// RefreshEpisodeMetadata writes the listing's title, overview, air date, runtime and still
// onto the episodes that already exist, by (season, episode), in one transaction — the
// metadata half of InsertSeasons. Monitoring, file state and absolute numbers are left
// alone. Only for a listing numbered the same way as the stored rows: otherwise one
// episode's title and date would land on another.
func (r *Repo) RefreshEpisodeMetadata(ctx context.Context, seriesID int64, seasons []Season) error {
	return store.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		for _, sn := range seasons {
			for _, ep := range sn.Episodes {
				if _, err := tx.ExecContext(ctx,
					`UPDATE episodes SET title = ?, overview = ?, air_date = ?, runtime = ?, still_url = ?
					 WHERE series_id = ? AND season_number = ? AND episode_number = ?`,
					ep.Title, ep.Overview, ep.AirDate, ep.Runtime, ep.StillURL,
					seriesID, sn.SeasonNumber, ep.EpisodeNumber); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// ReassignAbsolutes writes the listing's absolute numbers onto the episodes by (season,
// episode), in one transaction. For a show numbered by season and episode that's its
// identity, so when an earlier season gains or loses an episode, every later absolute
// shifts but nothing else does: each file stays on the (season, episode) it was on. It
// never reads or touches a file.
func (r *Repo) ReassignAbsolutes(ctx context.Context, seriesID int64, seasons []Season) error {
	return store.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		for _, sn := range seasons {
			for _, ep := range sn.Episodes {
				if _, err := tx.ExecContext(ctx,
					`UPDATE episodes SET absolute_number = ? WHERE series_id = ? AND season_number = ? AND episode_number = ?`,
					ep.AbsoluteNumber, seriesID, sn.SeasonNumber, ep.EpisodeNumber); err != nil {
					return err
				}
			}
		}
		return nil
	})
}

// StoredNumbering returns the series' current absolute → (season, episode) mapping, for
// episodes that carry an absolute number. Refresh compares it against fresh metadata to
// notice when a source has changed the season MODEL — the same absolute episode now at a
// different (season, episode) — which INSERT-OR-IGNORE can never correct on its own.
func (r *Repo) StoredNumbering(ctx context.Context, seriesID int64) (map[int][2]int, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT absolute_number, season_number, episode_number FROM episodes
		 WHERE series_id = ? AND absolute_number > 0`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int][2]int{}
	for rows.Next() {
		var abs, s, e int
		if err := rows.Scan(&abs, &s, &e); err != nil {
			return nil, err
		}
		out[abs] = [2]int{s, e}
	}
	return out, rows.Err()
}

// PruneSeasonsNotIn removes seasons the metadata no longer lists, and reports how many
// went. Nothing holding a file is ever touched.
//
// Needed because a metadata source can change a show's whole season MODEL, not just its
// contents — TVmaze numbers some long-running shows by broadcast year, so Naruto briefly
// gained seasons 2002 through 2007 alongside its real ones. Without pruning, a refresh
// could only ever ADD, so the wrong seasons sat there permanently and the only cure was
// deleting and re-adding the show.
//
// Deliberately conservative: an episode with a file survives whatever the metadata says,
// and a season keeping any such episode survives with it. Losing track of a file the user
// actually has is far worse than an extra row in the season list.
func (r *Repo) PruneSeasonsNotIn(ctx context.Context, seriesID int64, keep []int) (int, error) {
	if len(keep) == 0 {
		return 0, nil // no listing to trust — prune nothing
	}
	placeholders := strings.TrimSuffix(strings.Repeat("?,", len(keep)), ",")
	args := []any{seriesID}
	for _, n := range keep {
		args = append(args, n)
	}

	if _, err := r.db.ExecContext(ctx,
		`DELETE FROM episodes
		 WHERE series_id = ? AND has_file = 0 AND season_number NOT IN (`+placeholders+`)`, args...); err != nil {
		return 0, err
	}
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM seasons
		 WHERE series_id = ? AND season_number NOT IN (`+placeholders+`)
		   AND NOT EXISTS (SELECT 1 FROM episodes e
		                   WHERE e.series_id = seasons.series_id AND e.season_number = seasons.season_number)`, args...)
	if err != nil {
		return 0, err
	}
	n, _ := res.RowsAffected()
	return int(n), nil
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

// SetMonitored sets a series' monitored flag, which is a gate: off pauses the show (the
// sweep, RSS and upgrades skip it) and leaves every season and episode choice as it is,
// so resuming picks up exactly where the owner left off. It used to cascade both ways,
// and pausing then resuming re-monitored seasons the owner had switched off.
//
// One case still cascades: turning on a show with no monitored regular episode — a
// library-scanned show, added unmonitored — monitors every regular season and episode
// and new seasons, or it would read "Monitored" and never grab anything. Specials stay
// out of it.
func (r *Repo) SetMonitored(ctx context.Context, id int64, monitored bool) error {
	return store.WithTx(ctx, r.db, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `UPDATE series SET monitored = ? WHERE id = ?`, b2i(monitored), id); err != nil {
			return err
		}
		if !monitored {
			return nil
		}
		var one int
		err := tx.QueryRowContext(ctx,
			`SELECT 1 FROM episodes WHERE series_id = ? AND season_number > 0 AND monitored = 1 LIMIT 1`, id).Scan(&one)
		if err == nil {
			return nil // the owner's choices stand
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE seasons SET monitored = 1 WHERE series_id = ? AND season_number > 0`, id); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, `UPDATE episodes SET monitored = 1 WHERE series_id = ? AND season_number > 0`, id); err != nil {
			return err
		}
		_, err = tx.ExecContext(ctx, `UPDATE series SET monitor_new_seasons = 1 WHERE id = ?`, id)
		return err
	})
}

// SetMonitorNewSeasons sets whether a season new to the show is monitored when a refresh
// adds it.
func (r *Repo) SetMonitorNewSeasons(ctx context.Context, id int64, on bool) error {
	res, err := r.db.ExecContext(ctx, `UPDATE series SET monitor_new_seasons = ? WHERE id = ?`, b2i(on), id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SeasonMonitorFlags returns each stored season's monitored flag by season number.
func (r *Repo) SeasonMonitorFlags(ctx context.Context, seriesID int64) (map[int]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT season_number, monitored FROM seasons WHERE series_id = ?`, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]bool{}
	for rows.Next() {
		var n, mon int
		if err := rows.Scan(&n, &mon); err != nil {
			return nil, err
		}
		out[n] = mon != 0
	}
	return out, rows.Err()
}

// SeriesMeta is the show-level metadata a refresh brings up to date. A zero value means
// "the provider didn't say", never "clear it".
type SeriesMeta struct {
	Title, Overview, PosterURL, Status, Network string
	Year                                        int
	Extra                                       *SeriesExtra
}

// UpdateSeriesMetadata writes a refresh's show-level metadata, overwriting a column only
// with a non-empty fresh value: a provider that answers with half a record (a timeout on
// its credits call, a show it only partly knows) must not blank out what's stored. The
// extra blob is merged field by field the same way. Returns the row as it now stands.
func (r *Repo) UpdateSeriesMetadata(ctx context.Context, id int64, m SeriesMeta) (Series, error) {
	cur, err := r.Get(ctx, id)
	if err != nil {
		return Series{}, err
	}
	next := cur
	setStr := func(dst *string, v string) {
		if v = strings.TrimSpace(v); v != "" {
			*dst = v
		}
	}
	setStr(&next.Title, m.Title)
	setStr(&next.Overview, m.Overview)
	setStr(&next.PosterURL, m.PosterURL)
	setStr(&next.Status, m.Status)
	setStr(&next.Network, m.Network)
	if m.Year > 0 {
		next.Year = m.Year
	}
	next.Extra = mergeExtra(cur.Extra, m.Extra)
	extraJSON := ""
	if next.Extra != nil {
		b, err := json.Marshal(next.Extra)
		if err != nil {
			return Series{}, err
		}
		extraJSON = string(b)
	}
	if _, err := r.db.ExecContext(ctx,
		`UPDATE series SET title = ?, overview = ?, poster_url = ?, status = ?, network = ?, year = ?, extra_json = ?
		 WHERE id = ?`,
		next.Title, next.Overview, next.PosterURL, next.Status, next.Network, next.Year, extraJSON, id); err != nil {
		return Series{}, err
	}
	return next, nil
}

// mergeExtra lays a fresh extra blob over the stored one, keeping each stored field the
// fresh one leaves empty.
func mergeExtra(stored, fresh *SeriesExtra) *SeriesExtra {
	if fresh == nil {
		return stored
	}
	out := SeriesExtra{}
	if stored != nil {
		out = *stored
	}
	if len(fresh.Genres) > 0 {
		out.Genres = fresh.Genres
	}
	if fresh.BackdropURL != "" {
		out.BackdropURL = fresh.BackdropURL
	}
	if len(fresh.Cast) > 0 {
		out.Cast = fresh.Cast
	}
	if fresh.OriginalTitle != "" {
		out.OriginalTitle = fresh.OriginalTitle
	}
	if fresh.OriginalLanguage != "" {
		out.OriginalLanguage = fresh.OriginalLanguage
	}
	return &out
}

// MarkRefreshed stamps a successful metadata pull, so the weekly re-check of ended shows
// knows which are due.
func (r *Repo) MarkRefreshed(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE series SET last_refreshed_at = datetime('now') WHERE id = ?`, id)
	return err
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

// BackfillAbsolute fills in a 1-based ordinal (across the non-special seasons, ordered by
// season then episode) for episodes that DON'T already have an absolute number. It
// retro-fits series added before absolute numbering existed.
//
// It deliberately leaves non-zero absolutes alone. TVDB supplies authoritative absolute
// numbers — which for anime can differ from a naive positional count — and those are
// exactly what fansub releases ("Show - 137") are matched against. An earlier version
// recomputed every row unconditionally, silently overwriting TVDB's numbers with counted
// ones on every refresh; only unset rows are touched now.
func (r *Repo) BackfillAbsolute(ctx context.Context, seriesID int64) error {
	_, err := r.db.ExecContext(ctx,
		`UPDATE episodes SET absolute_number = (
			SELECT COUNT(*) FROM episodes e2
			WHERE e2.series_id = episodes.series_id AND e2.season_number > 0
			  AND (e2.season_number < episodes.season_number
			       OR (e2.season_number = episodes.season_number AND e2.episode_number <= episodes.episode_number))
		) WHERE series_id = ? AND season_number > 0 AND absolute_number = 0`, seriesID)
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

// SeasonEpisodeTitles returns every episode title in a season, keyed by episode number.
//
// Used to identify an episode by its TITLE rather than its number, for releases numbered
// against a different metadata source. TMDB and TVDB disagree constantly about two-part
// episodes — TMDB merges "London" into one 44-minute entry where TVDB splits it — which
// shifts every subsequent episode by one.
func (r *Repo) SeasonEpisodeTitles(ctx context.Context, seriesID int64, season int) map[int]string {
	rows, err := r.db.QueryContext(ctx,
		`SELECT episode_number, title FROM episodes
		 WHERE series_id = ? AND season_number = ? AND title != ''`, seriesID, season)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[int]string{}
	for rows.Next() {
		var n int
		var t string
		if rows.Scan(&n, &t) == nil {
			out[n] = t
		}
	}
	return out
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

// SearchCursors returns where the last sweep stopped in the season fan-out and in the
// anime absolute-number follow-up, so the next one resumes instead of restarting at the
// lowest season and starving everything past the query budget.
func (r *Repo) SearchCursors(ctx context.Context, seriesID int64) (season, absolute int) {
	_ = r.db.QueryRowContext(ctx,
		`SELECT search_season_cursor, search_abs_cursor FROM series WHERE id = ?`, seriesID).Scan(&season, &absolute)
	return season, absolute
}

// SetSeasonCursor records where the next season fan-out should resume.
func (r *Repo) SetSeasonCursor(ctx context.Context, seriesID int64, cursor int) {
	_, _ = r.db.ExecContext(ctx,
		`UPDATE series SET search_season_cursor = ? WHERE id = ?`, cursor, seriesID)
}

// SetAbsoluteCursor records where the next absolute-number follow-up should resume.
func (r *Repo) SetAbsoluteCursor(ctx context.Context, seriesID int64, cursor int) {
	_, _ = r.db.ExecContext(ctx,
		`UPDATE series SET search_abs_cursor = ? WHERE id = ?`, cursor, seriesID)
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
// ok=false when the series has no episode with that absolute number. Ordered so a
// duplicate absolute number (metadata divergence between the computed counter and
// TVDB-supplied values) resolves deterministically to the earliest episode instead
// of whichever row SQLite happens to return first.
func (r *Repo) EpisodeByAbsolute(ctx context.Context, seriesID int64, absolute int) (season, episode int, ok bool) {
	err := r.db.QueryRowContext(ctx,
		`SELECT season_number, episode_number FROM episodes
		 WHERE series_id = ? AND absolute_number = ?
		 ORDER BY season_number, episode_number LIMIT 1`,
		seriesID, absolute).Scan(&season, &episode)
	if err != nil {
		return 0, 0, false
	}
	return season, episode, true
}

// EpisodesSharingPath counts OTHER episodes (excluding the given one) whose file is
// the same on-disk path — siblings served by a multi-episode file.
func (r *Repo) EpisodesSharingPath(ctx context.Context, seriesID int64, path string, season, episode int) (int, error) {
	var n int
	err := r.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM episodes
		 WHERE series_id = ? AND file_path = ? AND has_file = 1
		   AND NOT (season_number = ? AND episode_number = ?)`,
		seriesID, path, season, episode).Scan(&n)
	return n, err
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

// Aliases returns a series' alternate release titles, oldest first.
func (r *Repo) Aliases(ctx context.Context, seriesID int64) []Alias {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id, title, tmdb_season FROM series_aliases WHERE series_id = ? ORDER BY id`, seriesID)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []Alias
	for rows.Next() {
		var a Alias
		if err := rows.Scan(&a.ID, &a.Title, &a.TMDBSeason); err != nil {
			continue
		}
		out = append(out, a)
	}
	return out
}

// AddAlias records an alternate title. Re-adding one that already exists updates its
// season rather than failing — the user is correcting it, not making a mistake.
func (r *Repo) AddAlias(ctx context.Context, seriesID int64, title, key string, season int) (Alias, error) {
	res, err := r.db.ExecContext(ctx,
		`INSERT INTO series_aliases (series_id, title, title_key, tmdb_season) VALUES (?, ?, ?, ?)
		 ON CONFLICT(series_id, title_key) DO UPDATE SET title = excluded.title, tmdb_season = excluded.tmdb_season`,
		seriesID, title, key, season)
	if err != nil {
		return Alias{}, err
	}
	id, _ := res.LastInsertId()
	return Alias{ID: id, Title: title, TMDBSeason: season}, nil
}

// DeleteAlias removes one alternate title. Scoped by series so an id from another
// series can't be deleted through it.
func (r *Repo) DeleteAlias(ctx context.Context, seriesID, aliasID int64) error {
	res, err := r.db.ExecContext(ctx,
		`DELETE FROM series_aliases WHERE series_id = ? AND id = ?`, seriesID, aliasID)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// SeasonEpisodeNumbers returns one season's episode numbers in order. Used to read an
// alias' continuous numbering ("episode 45 of the arc") as an index into the season,
// rather than assuming the season starts at 1 with no gaps.
func (r *Repo) SeasonEpisodeNumbers(ctx context.Context, seriesID int64, season int) []int {
	rows, err := r.db.QueryContext(ctx,
		`SELECT episode_number FROM episodes WHERE series_id = ? AND season_number = ?
		 ORDER BY episode_number`, seriesID, season)
	if err != nil {
		return nil
	}
	defer rows.Close()
	var out []int
	for rows.Next() {
		var n int
		if err := rows.Scan(&n); err != nil {
			continue
		}
		out = append(out, n)
	}
	return out
}

// AliasTitlesFor returns every series' alias keys in one query, so listing series
// doesn't cost a lookup per row.
func (r *Repo) AliasTitlesFor(ctx context.Context) map[int64][]Alias {
	rows, err := r.db.QueryContext(ctx, `SELECT series_id, id, title, tmdb_season FROM series_aliases ORDER BY series_id, id`)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[int64][]Alias{}
	for rows.Next() {
		var sid int64
		var a Alias
		if err := rows.Scan(&sid, &a.ID, &a.Title, &a.TMDBSeason); err != nil {
			continue
		}
		out[sid] = append(out[sid], a)
	}
	return out
}
