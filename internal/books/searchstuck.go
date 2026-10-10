package books

import "context"

// CountSearchGivenUp counts monitored books the search ladder has slowed to a monthly
// check (more than MonthlyAfter empty searches in a row) that still lack an edition: the
// Needs-you feed's "still haven't found a release". Which editions a book wants depends
// on its profile, so "still lacks one" is read as "doesn't have both"; a grab resets the
// counter, so a found book drops out on its own.
func (s *Service) CountSearchGivenUp(ctx context.Context) (int, error) {
	var n int
	err := s.repo.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM books WHERE monitored = 1 AND search_misses > ?
		   AND NOT (ebook_path != '' AND audiobook_path != '')`, MonthlyAfter).Scan(&n)
	return n, err
}
