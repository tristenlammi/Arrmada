package series

import (
	"context"
	"database/sql"
	"errors"
)

// SeasonProgress is one regular season's episode counts, for the per-season request
// states and for season-scoped requests' tracking.
type SeasonProgress struct {
	Episodes int // episode rows the library has for it
	Have     int // episodes with a file
	// Aired is what "all of it is here" counts against: episodes with a file, plus the
	// rest that have aired (a dated episode on or before today, as automation's aired()).
	Aired int
	// Monitored is the season's own flag.
	Monitored bool
	// Wanted are monitored (episode and season), aired episodes with no file: what a
	// search would grab. Upcoming are the monitored ones that haven't aired yet.
	Wanted, Upcoming int
	// MonHave and MonTotal are the completeness counts over monitored episodes only:
	// those with a file, and those with a file or aired. An episode the owner stopped
	// monitoring (one nobody can find) doesn't hold the season back.
	MonHave, MonTotal int
}

// OnDisk reports whether every aired episode of the season is on disk.
func (p SeasonProgress) OnDisk() bool { return p.Have > 0 && p.Have >= p.Aired }

// seasonProgressSQL is one row per regular season, announced seasons with no episodes
// yet included (all zeros). The aired rule is statsAiredSQL, the one the stats use.
const seasonProgressSQL = `
	SELECT sn.season_number, sn.monitored,
	  COUNT(e.id),
	  COALESCE(SUM(e.has_file), 0),
	  COALESCE(SUM(CASE WHEN e.has_file = 1 OR ` + statsAiredSQL + ` THEN 1 ELSE 0 END), 0),
	  COALESCE(SUM(CASE WHEN e.has_file = 0 AND e.monitored = 1 AND sn.monitored = 1 AND ` + statsAiredSQL + ` THEN 1 ELSE 0 END), 0),
	  COALESCE(SUM(CASE WHEN e.has_file = 0 AND e.monitored = 1 AND sn.monitored = 1 AND NOT ` + statsAiredSQL + ` THEN 1 ELSE 0 END), 0),
	  COALESCE(SUM(CASE WHEN e.monitored = 1 AND e.has_file = 1 THEN 1 ELSE 0 END), 0),
	  COALESCE(SUM(CASE WHEN e.monitored = 1 AND (e.has_file = 1 OR ` + statsAiredSQL + `) THEN 1 ELSE 0 END), 0)
	FROM seasons sn
	LEFT JOIN episodes e ON e.series_id = sn.series_id AND e.season_number = sn.season_number
	WHERE sn.series_id = ? AND sn.season_number > 0
	GROUP BY sn.season_number`

// SeasonProgress returns each regular season's counts by season number, in one query.
func (r *Repo) SeasonProgress(ctx context.Context, seriesID int64) (map[int]SeasonProgress, error) {
	rows, err := r.db.QueryContext(ctx, seasonProgressSQL, seriesID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int]SeasonProgress{}
	for rows.Next() {
		var n, mon int
		var p SeasonProgress
		if err := rows.Scan(&n, &mon, &p.Episodes, &p.Have, &p.Aired, &p.Wanted, &p.Upcoming, &p.MonHave, &p.MonTotal); err != nil {
			return nil, err
		}
		p.Monitored = mon != 0
		out[n] = p
	}
	return out, rows.Err()
}

// SeasonProgress returns each regular season's counts (see Repo.SeasonProgress).
func (s *Service) SeasonProgress(ctx context.Context, seriesID int64) (map[int]SeasonProgress, error) {
	return s.repo.SeasonProgress(ctx, seriesID)
}

// GetByTMDB returns the library series with a TMDB id (ErrNotFound when there is none).
// No seasons or stats are attached.
func (r *Repo) GetByTMDB(ctx context.Context, tmdbID int) (Series, error) {
	row := r.db.QueryRowContext(ctx, `SELECT `+seriesCols+` FROM series WHERE tmdb_id = ?`, tmdbID)
	s, err := scanSeries(row)
	if errors.Is(err, sql.ErrNoRows) {
		return Series{}, ErrNotFound
	}
	return s, err
}

// GetByTMDB returns the library series with a TMDB id (ErrNotFound when there is none).
func (s *Service) GetByTMDB(ctx context.Context, tmdbID int) (Series, error) {
	return s.repo.GetByTMDB(ctx, tmdbID)
}
