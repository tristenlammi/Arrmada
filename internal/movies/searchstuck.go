package movies

import "context"

// CountSearchStuck counts monitored films still without a file whose missing-sweep has
// come up empty at least misses times in a row: the Needs-you feed's "still haven't found
// a release". One COUNT on the movies table (monitored is indexed).
func (s *Service) CountSearchStuck(ctx context.Context, misses int) (int, error) {
	var n int
	err := s.repo.q().QueryRowContext(ctx,
		`SELECT COUNT(*) FROM movies WHERE monitored = 1 AND has_file = 0 AND search_misses >= ?`, misses).Scan(&n)
	return n, err
}
