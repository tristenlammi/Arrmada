package series

import "context"

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
