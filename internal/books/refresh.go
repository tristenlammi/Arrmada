package books

import (
	"context"
	"errors"
	"fmt"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Catalogue freshness. Books added from an author's catalogue or 'Add missing in series'
// are created from the list entry alone — no description, genres or series — and the only
// thing that ever went back for them was the manual Refresh button. The refresh-books job
// (RefreshDue) now works through the library a few books at a time, and books added in
// bulk are refreshed before their first search (RefreshBooks), so the search carries the
// catalogue's author and series.
//
// Hardcover's daily request budget is shared with Discover and people browsing, so all of
// this background work holds off once most of the day's budget is gone.

const (
	// HardcoverBackgroundPct is the share of Hardcover's daily budget that background
	// catalogue work may use; past it, it waits for tomorrow and leaves the rest for
	// people. Also the gate for the author release check (BOOK-21).
	HardcoverBackgroundPct = 70

	// RefreshStaleDays is how old a book's last refresh may get before the job looks
	// again (descriptions, series and release dates do change).
	RefreshStaleDays = 30

	// SkippedHardcoverBudget is RefreshDue's skip reason when the budget gate held it back.
	SkippedHardcoverBudget = "Hardcover budget"
)

// hardcoverUsage is the metadata provider's view of today's Hardcover budget
// (metadata.BookSources). A provider without it has no budget to mind.
type hardcoverUsage interface {
	HardcoverUsage() (used, budget int)
}

// HardcoverBudgetLow reports whether background catalogue work should stop for today:
// Hardcover is configured and more than HardcoverBackgroundPct of its daily budget is
// used. False when Hardcover isn't in use. Safe to call before every request of a long
// job; it reads a counter, it doesn't ask Hardcover.
//
// For BOOK-21's CheckAuthorReleases: check it before each author, and stop the run
// (logging "skipped: Hardcover budget") when it turns true.
func (s *Service) HardcoverBudgetLow() bool {
	hu, ok := s.meta.(hardcoverUsage)
	if !ok {
		return false
	}
	used, budget := hu.HardcoverUsage()
	return budget > 0 && used*100 > budget*HardcoverBackgroundPct
}

// RefreshDue refreshes up to limit books from the catalogue: never-refreshed books
// first, then those not refreshed in RefreshStaleDays, oldest first. It stops early, with
// skipped = SkippedHardcoverBudget, when the Hardcover budget gate closes (before the run
// or part way through) or Hardcover refuses a request for budget; err is a database read
// failure or the context ending. Each book that was looked up is stamped, found or not,
// so one key the catalogue no longer knows can't hold the front of the queue forever.
func (s *Service) RefreshDue(ctx context.Context, limit int) (refreshed int, skipped string, err error) {
	if limit <= 0 || s.meta == nil {
		return 0, "", nil
	}
	if s.HardcoverBudgetLow() {
		return 0, SkippedHardcoverBudget, nil
	}
	ids, err := s.repo.DueForRefresh(ctx, RefreshStaleDays, limit)
	if err != nil {
		return 0, "", err
	}
	for _, id := range ids {
		if s.HardcoverBudgetLow() {
			return refreshed, SkippedHardcoverBudget, nil
		}
		_, fetchErr, err := s.refresh(ctx, id)
		switch {
		case errors.Is(fetchErr, metadata.ErrHardcoverBudget):
			return refreshed, SkippedHardcoverBudget, nil
		case ctx.Err() != nil:
			return refreshed, "", ctx.Err()
		case errors.Is(err, ErrNotFound):
			continue // deleted since the list was read
		case err != nil:
			return refreshed, "", err
		}
		if fetchErr == nil {
			refreshed++
		}
	}
	return refreshed, "", nil
}

// RefreshBooks refreshes books that were just added from a catalogue list (an author's
// works, the rest of a series), before their first search, and returns how many it
// refreshed. It is budget-aware: when the Hardcover gate closes it stops, and the books
// it didn't reach are searched as they are and refreshed later by the job.
func (s *Service) RefreshBooks(ctx context.Context, ids []int64) int {
	if s.meta == nil {
		return 0 // no catalogue wired (tools, tests): nothing to refresh from
	}
	n := 0
	for _, id := range ids {
		if ctx.Err() != nil || s.HardcoverBudgetLow() {
			break
		}
		_, fetchErr, err := s.refresh(ctx, id)
		if errors.Is(fetchErr, metadata.ErrHardcoverBudget) {
			break
		}
		if err == nil && fetchErr == nil {
			n++
		}
	}
	return n
}

// DueForRefresh lists the books the refresh job should look at next: never refreshed
// (an empty stamp sorts first), then refreshed more than staleDays ago, oldest first.
func (r *Repo) DueForRefresh(ctx context.Context, staleDays, limit int) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx,
		`SELECT id FROM books WHERE meta_checked_at = '' OR meta_checked_at < `+store.UTCNowText(r.db, "?")+`
		  ORDER BY meta_checked_at, id LIMIT ?`, fmt.Sprintf("-%d days", staleDays), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// StampMetaChecked records that a book's catalogue entry was just looked up.
func (r *Repo) StampMetaChecked(ctx context.Context, id int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE books SET meta_checked_at = `+store.UTCNowText(r.db, "")+` WHERE id = ?`, id)
	return err
}
