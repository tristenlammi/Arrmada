package series

import "context"

// SearchStamp is where one show stands on the missing-sweep's backoff.
type SearchStamp struct {
	LastAt string // when the sweep last searched it, as stored ("" = never)
	Misses int    // sweeps in a row that grabbed nothing
}

// SearchStates is every show's search state in one query, for a list (a requester's
// "last checked" line).
func (s *Service) SearchStates(ctx context.Context) (map[int64]SearchStamp, error) {
	rows, err := s.repo.db.QueryContext(ctx, `SELECT id, last_search_at, search_misses FROM series`)
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

// CountSearchStuck counts monitored shows that still have an aired, monitored episode
// missing and whose missing-sweep has come up empty at least misses times in a row: the
// Needs-you feed's "still haven't found a release". The episode check is an EXISTS on
// the series_id index, so the cost is one probe per show past the threshold.
func (s *Service) CountSearchStuck(ctx context.Context, misses int) (int, error) {
	var n int
	err := s.repo.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM series s
		WHERE s.monitored = 1 AND s.search_misses >= ?
		  AND EXISTS (SELECT 1 FROM episodes e WHERE e.series_id = s.id AND e.monitored = 1
		              AND e.has_file = 0 AND e.season_number > 0
		              AND e.air_date != '' AND e.air_date <= date('now'))`, misses).Scan(&n)
	return n, err
}
