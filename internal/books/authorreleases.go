package books

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

// New books by followed authors.
//
// "Add author" used to take one snapshot of the author's catalogue and never look again,
// so a followed author's next book only arrived if someone noticed it. The
// check-author-releases job now asks the catalogue about each followed author about once
// a week and adds the works it hasn't seen before.
//
// What counts as new is author_works_seen, not the library: the first look at an author
// only records what the catalogue lists (following someone never re-adds back catalogue
// the admin chose to skip), and a book the job added and someone then deleted stays
// seen, so it doesn't come back. A work is seen by its catalogue key and by its
// normalised title, so the same book listed under a new key is still the same book.

const (
	// AuthorRecheckDays is how often one followed author's catalogue is looked at.
	AuthorRecheckDays = 7

	// authorRetryDays is when an author whose catalogue couldn't be read is tried again:
	// a day, not a week, but not straight away either, so one broken key can't keep the
	// front of the queue.
	authorRetryDays = 1

	// maxNewWorksPerCheck: more new works than this for one author in one week isn't a
	// writer's output, it's the catalogue changing under us (a listing that stopped
	// filtering translations, a merge of author records). Nothing is added and nothing
	// recorded; the next check sees whether it settled.
	maxNewWorksPerCheck = 10

	// titleSeenPrefix marks a seen-set entry that is a normalised title, not a key.
	titleSeenPrefix = "title:"
)

// AuthorRelease is the books one check added for one author.
type AuthorRelease struct {
	AuthorID int64
	Author   string
	Books    []Book
}

// AuthorCheck is what one run of CheckAuthorReleases did.
type AuthorCheck struct {
	Checked  int             // authors whose catalogue was read
	Seeded   int             // of those, first looks: recorded, nothing added
	Releases []AuthorRelease // authors with new books, and those books
	// Skipped is SkippedHardcoverBudget when the budget gate stopped the run.
	Skipped string
}

// Added is how many books the run added, over every author.
func (c AuthorCheck) Added() int {
	n := 0
	for _, r := range c.Releases {
		n += len(r.Books)
	}
	return n
}

// seenKeys are the keys a work is recorded under: its catalogue key and its title.
func seenKeys(wk metadata.BookResult) []string {
	var out []string
	if k := strings.TrimSpace(wk.Key); k != "" {
		out = append(out, k)
	}
	if t := IdentityOf(wk.Title, "").Full; t != "" {
		out = append(out, titleSeenPrefix+t)
	}
	return out
}

// worksSeen is an author's seen set.
func (r *Repo) worksSeen(ctx context.Context, authorID int64) (map[string]bool, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT work_key FROM author_works_seen WHERE author_id = ?`, authorID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		out[k] = true
	}
	return out, rows.Err()
}

// markSeen records works as seen for an author, and optionally stamps the author as
// checked (and seeded) now, in one transaction: a seed that half-landed would make the
// rest of the catalogue look new next week.
func (r *Repo) markSeen(ctx context.Context, authorID int64, works []metadata.BookResult, stamp bool) error {
	return store.WithTx(ctx, r.db, func(tx *store.Transaction) error {
		for _, wk := range works {
			for _, k := range seenKeys(wk) {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO author_works_seen (author_id, work_key) VALUES (?, ?) ON CONFLICT DO NOTHING`, authorID, k); err != nil {
					return err
				}
			}
		}
		if !stamp {
			return nil
		}
		_, err := tx.ExecContext(ctx, `UPDATE authors SET works_checked_at = `+store.UTCNowText(tx, "")+`, works_seeded = 1 WHERE id = ?`, authorID)
		return err
	})
}

// stampWorksRetry marks an author whose catalogue couldn't be read, so the job comes
// back to them in authorRetryDays rather than a week (or at once). It leaves
// works_seeded alone: an author whose first look failed still gets a first look.
func (r *Repo) stampWorksRetry(ctx context.Context, authorID int64) error {
	_, err := r.db.ExecContext(ctx, `UPDATE authors SET works_checked_at = `+store.UTCNowText(r.db, "?")+` WHERE id = ?`,
		fmt.Sprintf("-%d days", AuthorRecheckDays-authorRetryDays), authorID)
	return err
}

// authorsDueForWorks lists the followed authors whose catalogue is due a look: monitored,
// adding new books, with a catalogue key to ask about, never checked or checked over
// AuthorRecheckDays ago, oldest first.
func (r *Repo) authorsDueForWorks(ctx context.Context, limit int) ([]Author, error) {
	rows, err := r.db.QueryContext(ctx, `SELECT `+authorCols+` FROM authors a
		WHERE a.monitored = 1 AND a.monitor_new = 1 AND a.catalogue_key != ''
		  AND (a.works_checked_at = '' OR a.works_checked_at < `+store.UTCNowText(r.db, "?")+`)
		ORDER BY a.works_checked_at, a.id LIMIT ?`, fmt.Sprintf("-%d days", AuthorRecheckDays), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Author
	for rows.Next() {
		a, err := scanAuthor(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SeedAuthorWorks records an author's catalogue as seen and stamps them checked —
// "Add author" calls it with the list it just added from, so a book that comes out
// before the job's first look at the author is still new to it.
func (s *Service) SeedAuthorWorks(ctx context.Context, authorID int64, works []metadata.BookResult) error {
	if authorID <= 0 || len(works) == 0 {
		return nil
	}
	return s.repo.markSeen(ctx, authorID, works, true)
}

// backCatalogue reports whether a work the catalogue only now lists is an old book, not
// a new one: its year is known and before last year. Catalogues surface old entries as
// they gain readers or editions; those are marked seen rather than added.
func backCatalogue(wk metadata.BookResult, now time.Time) bool {
	if day := ReleaseDay(wk.ReleaseDate); !day.IsZero() {
		return day.Year() < now.Year()-1
	}
	return wk.Year > 0 && wk.Year < now.Year()-1
}

// CheckAuthorReleases looks at up to limit followed authors' catalogues (oldest check
// first, each about once a week) and adds their new books, monitored, with the author's
// profile (defaultProfile when they have none). A first look only records the
// catalogue. It stops, with Skipped = SkippedHardcoverBudget, when the Hardcover budget
// gate closes before an author or Hardcover refuses for budget. New books are refreshed
// from the catalogue (budget allowing) before they are returned, so the caller's search
// carries the catalogue's author and series and knows the release date; err is a
// database failure or the context ending.
func (s *Service) CheckAuthorReleases(ctx context.Context, limit int, defaultProfile string) (AuthorCheck, error) {
	var res AuthorCheck
	if limit <= 0 || s.meta == nil {
		return res, nil
	}
	due, err := s.repo.authorsDueForWorks(ctx, limit)
	if err != nil {
		return res, err
	}
	for _, a := range due {
		if ctx.Err() != nil {
			return res, ctx.Err()
		}
		if s.HardcoverBudgetLow() {
			res.Skipped = SkippedHardcoverBudget
			return res, nil
		}
		works, err := s.meta.AuthorWorks(ctx, a.CatalogueKey, 0)
		switch {
		case errors.Is(err, metadata.ErrHardcoverBudget):
			res.Skipped = SkippedHardcoverBudget
			return res, nil
		case ctx.Err() != nil:
			return res, ctx.Err()
		case err != nil:
			// The provider's own words, so a query Hardcover rejects shows why in the log.
			s.log.Info("author releases: catalogue lookup failed", "author_id", a.ID, "author_key", a.CatalogueKey, "err", err)
			if err := s.repo.stampWorksRetry(ctx, a.ID); err != nil {
				return res, err
			}
			continue
		}
		works = usableWorks(works)
		if len(works) == 0 {
			// An empty list is a catalogue that didn't answer properly, not an author with
			// no books; seeding from it would make their whole catalogue new next time.
			s.log.Info("author releases: the catalogue listed no books", "author_id", a.ID, "author_key", a.CatalogueKey)
			if err := s.repo.stampWorksRetry(ctx, a.ID); err != nil {
				return res, err
			}
			continue
		}
		res.Checked++
		if !a.worksSeeded {
			if err := s.repo.markSeen(ctx, a.ID, works, true); err != nil {
				return res, err
			}
			res.Seeded++
			continue
		}
		added, err := s.addNewWorks(ctx, a, works, orStr(a.QualityProfile, defaultProfile))
		if err != nil {
			return res, err
		}
		if len(added) > 0 {
			res.Releases = append(res.Releases, AuthorRelease{AuthorID: a.ID, Author: a.Name, Books: added})
		}
	}
	return res, nil
}

// usableWorks drops entries with no key or no title.
func usableWorks(in []metadata.BookResult) []metadata.BookResult {
	out := in[:0:0]
	for _, wk := range in {
		if strings.TrimSpace(wk.Key) != "" && strings.TrimSpace(wk.Title) != "" {
			out = append(out, wk)
		}
	}
	return out
}

// addNewWorks is one later check of one author: the works not seen before that the
// library doesn't hold and that are new books are added; everything listed is then
// seen, and the author stamped.
func (s *Service) addNewWorks(ctx context.Context, a Author, works []metadata.BookResult, profile string) ([]Book, error) {
	seen, err := s.repo.worksSeen(ctx, a.ID)
	if err != nil {
		return nil, err
	}
	list, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	have := NewIdentityIndex(list)
	now := time.Now()
	var fresh []metadata.BookResult
	held, old := 0, 0
	for _, wk := range works {
		known := false
		for _, k := range seenKeys(wk) {
			known = known || seen[k]
		}
		if known {
			continue
		}
		if _, ok := s.repo.BookIDForKey(ctx, wk.Key); ok {
			held++
			continue
		}
		if _, ok := have.Find(wk.Title, wk.Author); ok {
			held++
			continue
		}
		if _, ok := have.Find(wk.Title, a.Name); ok {
			held++
			continue
		}
		if backCatalogue(wk, now) {
			old++
			continue
		}
		fresh = append(fresh, wk)
	}
	if len(fresh) > maxNewWorksPerCheck {
		s.log.Warn("author releases: too many new books at once — looks like a catalogue change, adding none this week",
			"author_id", a.ID, "author_key", a.CatalogueKey, "new", len(fresh), "listed", len(works))
		// The held and old works are still recorded; the fresh ones wait for next week.
		var rest []metadata.BookResult
		freshKey := map[string]bool{}
		for _, wk := range fresh {
			freshKey[wk.Key] = true
		}
		for _, wk := range works {
			if !freshKey[wk.Key] {
				rest = append(rest, wk)
			}
		}
		return nil, s.repo.markSeen(ctx, a.ID, rest, true)
	}
	var added []Book
	if len(fresh) > 0 {
		added, _ = s.addWorksNoted(ctx, fresh, profile, true, a.ID, "New from "+a.Name)
	}
	// Seen from now on, added or not: a book someone deletes later stays gone.
	if err := s.repo.markSeen(ctx, a.ID, works, true); err != nil {
		return added, err
	}
	if len(added) == 0 {
		if held+old > 0 {
			s.log.Debug("author releases: nothing new", "author_id", a.ID, "already_held", held, "older_books", old)
		}
		return nil, nil
	}
	if _, err := s.repo.db.ExecContext(ctx,
		`UPDATE authors SET new_books_found = new_books_found + ? WHERE id = ?`, len(added), a.ID); err != nil {
		s.log.Warn("author releases: couldn't count the new books", "author_id", a.ID, "err", err)
	}
	ids := make([]int64, len(added))
	for i, b := range added {
		ids[i] = b.ID
	}
	// Fill in descriptions, series and the release date before the first search; the
	// refresh-books job catches whatever the budget gate leaves.
	s.RefreshBooks(ctx, ids)
	for i, b := range added {
		if got, err := s.repo.Get(ctx, b.ID); err == nil {
			added[i] = got
		}
	}
	s.log.Info("author releases: added new books", "author_id", a.ID, "added", len(added))
	return added, nil
}
