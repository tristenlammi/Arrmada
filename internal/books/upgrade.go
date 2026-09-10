package books

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// Upgrading the library to Hardcover.
//
// Books added before a Hardcover key went in carry Open Library (or Google Books)
// keys, so they still refresh from there — no series, the odd duplicate work, weaker
// covers. The upgrade re-matches each of them to Hardcover by title and author,
// rewrites the row to the Hardcover entry (key, description, cover, subjects, series)
// and, when two old rows land on the same Hardcover book, folds them into one.
// Files, monitoring and the quality profile are never touched.
//
// It runs by itself: at startup and when the key is saved, whenever books are left on
// other keys. The button on the Books page is the retry for whatever didn't match.

// UpgradeStatus is the background job's progress, for the Books page.
type UpgradeStatus struct {
	Running   bool     `json:"running"`
	Total     int      `json:"total"`
	Done      int      `json:"done"`
	Upgraded  int      `json:"upgraded"`
	Merged    int      `json:"merged"`
	Unmatched int      `json:"unmatched"`
	StartedAt int64    `json:"started_at,omitempty"`
	EndedAt   int64    `json:"ended_at,omitempty"`
	Error     string   `json:"error,omitempty"`
	Notes     []string `json:"notes,omitempty"` // why the first few books didn't match
	// Left is every book the run left as it was, with why, so the page can offer to
	// keep each one that way and stop reporting it.
	Left []UnmatchedBook `json:"left,omitempty"`
}

// UnmatchedBook is one book a re-match run couldn't place.
type UnmatchedBook struct {
	ID     int64  `json:"id"`
	Title  string `json:"title"`
	Author string `json:"author,omitempty"`
	Reason string `json:"reason"`
}

type upgradeState struct {
	mu     sync.Mutex
	status UpgradeStatus
}

const upgradeNoteLimit = 8

// Upgradable counts the books not yet on Hardcover keys.
func (s *Service) Upgradable(ctx context.Context) int {
	list, err := s.repo.List(ctx)
	if err != nil {
		return 0
	}
	n := 0
	for _, b := range list {
		if !metadata.IsHardcoverKey(b.OLKey) && !b.KeepCatalogue {
			n++
		}
	}
	return n
}

// UpgradeStatus reports the job's progress.
func (s *Service) UpgradeStatus() UpgradeStatus {
	s.upgrade.mu.Lock()
	defer s.upgrade.mu.Unlock()
	return s.upgrade.status
}

// StartUpgrade begins re-matching in the background. False when one is already running
// or Hardcover isn't the current source.
func (s *Service) StartUpgrade(ctx context.Context) bool {
	if s.MetadataSource() != metadata.SourceHardcover {
		return false
	}
	s.upgrade.mu.Lock()
	if s.upgrade.status.Running {
		s.upgrade.mu.Unlock()
		return false
	}
	s.upgrade.status = UpgradeStatus{Running: true, StartedAt: time.Now().Unix()}
	s.upgrade.mu.Unlock()
	go s.runUpgrade(ctx)
	return true
}

// MaybeStartUpgrade runs the upgrade when Hardcover is the source and books are still
// on other keys — called at startup and when the key is saved.
func (s *Service) MaybeStartUpgrade(ctx context.Context) bool {
	if s.MetadataSource() != metadata.SourceHardcover || s.Upgradable(ctx) == 0 {
		return false
	}
	return s.StartUpgrade(ctx)
}

func (s *Service) setUpgrade(fn func(*UpgradeStatus)) {
	s.upgrade.mu.Lock()
	fn(&s.upgrade.status)
	s.upgrade.mu.Unlock()
}

func (s *Service) runUpgrade(ctx context.Context) {
	defer s.setUpgrade(func(st *UpgradeStatus) { st.Running = false; st.EndedAt = time.Now().Unix() })
	list, err := s.repo.List(ctx)
	if err != nil {
		s.setUpgrade(func(st *UpgradeStatus) { st.Error = err.Error() })
		return
	}
	var todo []Book
	for _, b := range list {
		if !metadata.IsHardcoverKey(b.OLKey) && !b.KeepCatalogue {
			todo = append(todo, b)
		}
	}
	s.setUpgrade(func(st *UpgradeStatus) { st.Total = len(todo) })
	s.log.Info("books: upgrade to Hardcover started", "books", len(todo))
	for _, b := range todo {
		if ctx.Err() != nil {
			s.setUpgrade(func(st *UpgradeStatus) { st.Error = "stopped" })
			return
		}
		outcome, reason, err := s.upgradeOne(ctx, b)
		if err != nil && strings.Contains(err.Error(), "rate limited") {
			// The provider already waited and retried; a limit still in force means
			// something else is hammering it. Pause a minute and try this book again.
			select {
			case <-ctx.Done():
			case <-time.After(time.Minute):
				outcome, reason, err = s.upgradeOne(ctx, b)
			}
		}
		if err != nil {
			// A budget or schema error would fail every remaining book the same way:
			// stop and say so, rather than report a hundred "unmatched".
			s.log.Warn("books: upgrade stopped", "title", b.Title, "err", err)
			s.setUpgrade(func(st *UpgradeStatus) { st.Error = err.Error() })
			return
		}
		s.setUpgrade(func(st *UpgradeStatus) {
			st.Done++
			switch outcome {
			case "upgraded":
				st.Upgraded++
			case "merged":
				st.Merged++
			default:
				st.Unmatched++
				if len(st.Notes) < upgradeNoteLimit {
					st.Notes = append(st.Notes, fmt.Sprintf("%s — %s: %s", b.Title, orStr(b.Author, "no author"), reason))
				}
				st.Left = append(st.Left, UnmatchedBook{ID: b.ID, Title: b.Title, Author: b.Author, Reason: reason})
			}
		})
		if outcome == "unmatched" {
			s.log.Info("books: upgrade left a book as is", "title", b.Title, "author", b.Author, "reason", reason)
		}
	}
	st := s.UpgradeStatus()
	s.log.Info("books: upgrade to Hardcover finished", "total", len(todo), "upgraded", st.Upgraded, "merged", st.Merged, "unmatched", st.Unmatched)
}

// upgradeOne re-matches a single book. Outcomes: "upgraded", "merged", "unmatched"
// (with a reason). A returned error is one that would stop every book.
func (s *Service) upgradeOne(ctx context.Context, b Book) (outcome, reason string, err error) {
	sl, ok := s.meta.(sourceLookup)
	if !ok {
		return "unmatched", "the catalogue can't be searched", nil
	}
	search := func(q string) ([]metadata.BookResult, error) {
		r, err := sl.SearchBooksFrom(ctx, metadata.SourceHardcover, q)
		if err != nil {
			if errors.Is(err, metadata.ErrHardcoverBudget) || strings.Contains(err.Error(), "rejected") {
				return nil, err
			}
			return nil, fmt.Errorf("search failed: %w", err)
		}
		return r, nil
	}
	note := ""
	var match *metadata.BookResult
	var results []metadata.BookResult
	// A row whose title reads "<work> by <someone else>" is a study guide or summary
	// that a scan once filed as the book itself. The user's files are the book, so
	// look the book up rather than the guide.
	if work, by, ok := derivedWork(b.Title, b.Author); ok {
		r, err := search(work + " " + by)
		if err != nil {
			return "", "", err
		}
		if match = matchUpgrade(Book{Title: work, Author: by}, r); match != nil {
			note = fmt.Sprintf("re-identified as %q by %s (the record was a guide to it)", work, by)
		}
	}
	if match == nil {
		var err error
		results, err = search(strings.TrimSpace(b.Title + " " + b.Author))
		if err != nil {
			return "", "", err
		}
		match = matchUpgrade(b, results)
	}
	if match == nil && b.Author != "" {
		// The combined query can miss when the catalogue spells the author
		// differently; the title alone plus a check on the author usually lands it.
		more, err := search(b.Title)
		if err != nil {
			return "", "", err
		}
		match = matchUpgrade(b, more)
		if match == nil && len(more) > 0 {
			results = more
		}
	}
	if match == nil {
		if len(results) == 0 {
			return "unmatched", "Hardcover returned no results", nil
		}
		top := results[0]
		return "unmatched", fmt.Sprintf("no result matched (closest: %q by %s)", top.Title, orStr(top.Author, "unknown")), nil
	}
	d, err := s.meta.GetBook(ctx, match.Key)
	if err != nil {
		if errors.Is(err, metadata.ErrHardcoverBudget) {
			return "", "", err
		}
		return "unmatched", "matched " + match.Key + " but fetching it failed: " + err.Error(), nil
	}
	// Another row already sits on this Hardcover book: this one is its duplicate.
	if other, ok := s.findByKey(ctx, d.Key); ok && other.ID != b.ID {
		keeper, dup := other, b
		if editionCount(b) > editionCount(other) {
			keeper, dup = b, other
			// Point the keeper at the Hardcover entry before folding the other in.
			if err := s.applyUpgrade(ctx, keeper, d); err != nil {
				return "unmatched", "could not rewrite the row: " + err.Error(), nil
			}
		}
		if err := s.foldInto(ctx, keeper, dup); err != nil {
			return "unmatched", "could not merge with its duplicate: " + err.Error(), nil
		}
		return "merged", note, nil
	}
	if err := s.applyUpgrade(ctx, b, d); err != nil {
		return "unmatched", "could not rewrite the row: " + err.Error(), nil
	}
	if note != "" {
		s.repo.AddEvent(ctx, b.ID, "upgraded", "Re-matched: "+note)
	}
	return "upgraded", note, nil
}

// byAuthorRe reads "<work> by <Author Name>" off the end of a title: the author is up
// to four capitalised words or initials.
var byAuthorRe = regexp.MustCompile(`^(.*\S)\s+(?i:by)\s+((?:[A-Z][\w.'’\-]*)(?:\s+[A-Z][\w.'’\-]*){1,3})$`)

// guideRe marks a title as being about a book rather than the book.
var guideRe = regexp.MustCompile(`(?i)\b(?:guide|summary|summaries|study|analysis|notes|workbook|companion|lesson|lessons|sparknotes|cliffsnotes|litcharts)\b`)

// derivedWork returns the book a guide-like record is about, and that book's author,
// when the record's title names an author the record's own author isn't:
// "LinguiSystems novel guide for Harry Potter and the Goblet of Fire by J.K. Rowling"
// by Laura Sauser is a guide; the work is the Rowling novel.
func derivedWork(title, author string) (work, by string, ok bool) {
	m := byAuthorRe.FindStringSubmatch(strings.TrimSpace(title))
	if m == nil {
		return "", "", false
	}
	work, by = strings.TrimSpace(m[1]), strings.TrimSpace(m[2])
	guide := guideRe.MatchString(work)
	if authorsOverlap(author, by) && !guide {
		return "", "", false // "Dune by Frank Herbert" by Frank Herbert is just the book
	}
	if loc := guideRe.FindStringIndex(work); loc != nil {
		rest := strings.TrimSpace(work[loc[1]:])
		for _, prep := range []string{"for ", "to ", "of ", "on "} {
			if strings.HasPrefix(strings.ToLower(rest), prep) {
				rest = strings.TrimSpace(rest[len(prep):])
				break
			}
		}
		work = rest
	}
	work = strings.Trim(work, " -—:,")
	return work, by, work != ""
}

// AuthorsOverlap is authorsOverlap for other packages (the library scan).
func AuthorsOverlap(a, b string) bool { return authorsOverlap(a, b) }

// matchUpgrade picks the result that is the same book: the same title-and-author key
// first; failing that the same title with an author that shares a real word (so
// "Hugh Howey" matches "Howey, Hugh" and an initial-only rendering); failing that,
// with no author to check, the sole result of that title.
func matchUpgrade(b Book, results []metadata.BookResult) *metadata.BookResult {
	want := DedupeKey(b.Title, b.Author)
	for i := range results {
		if DedupeKey(results[i].Title, results[i].Author) == want {
			return &results[i]
		}
	}
	keys := titleKeys(b.Title)
	if keys[0] == "" {
		return nil
	}
	// The same title read either way round: a library "The Final Empire" is the
	// catalogue's "Mistborn: The Final Empire", and vice versa.
	var sameTitle []*metadata.BookResult
	for i := range results {
		if keysOverlap(keys, titleKeys(results[i].Title)) {
			sameTitle = append(sameTitle, &results[i])
		}
	}
	for _, r := range sameTitle {
		if authorsOverlap(b.Author, r.Author) {
			return r
		}
	}
	if len(sameTitle) == 0 {
		return nil
	}
	// No author to check against — none on the library side, or none on any of the
	// candidates — the first same-title result is the best the catalogue offers.
	authorless := true
	for _, r := range sameTitle {
		if strings.TrimSpace(r.Author) != "" {
			authorless = false
		}
	}
	if strings.TrimSpace(b.Author) == "" || authorless {
		return sameTitle[0]
	}
	return nil
}

// authorsOverlap reports whether two author strings share a word of three or more
// letters ("Hugh Howey" / "Howey, Hugh" / "H. Howey").
func authorsOverlap(a, b string) bool {
	wa := strings.Fields(wordKey(a))
	wb := strings.Fields(wordKey(b))
	for _, x := range wa {
		if len(x) < 3 {
			continue
		}
		for _, y := range wb {
			if x == y {
				return true
			}
		}
	}
	return false
}

// applyUpgrade rewrites a row to the Hardcover entry, keeping what the user set by
// hand: a custom cover (served from the app), and the year when Hardcover lacks one.
func (s *Service) applyUpgrade(ctx context.Context, b Book, d *metadata.BookDetails) error {
	nb := Book{
		OLKey: d.Key, Title: orStr(d.Title, b.Title), Author: orStr(d.Author, b.Author), Year: d.Year,
		CoverURL: orStr(d.CoverURL, b.CoverURL), Description: orStr(d.Description, b.Description), Subjects: d.Subjects,
	}
	if nb.Year == 0 {
		nb.Year = b.Year
	}
	if strings.HasPrefix(b.CoverURL, "/") { // an uploaded cover — the user chose it
		nb.CoverURL = b.CoverURL
	}
	if len(nb.Subjects) == 0 {
		nb.Subjects = b.Subjects
	}
	if err := s.repo.Rematch(ctx, b.ID, nb); err != nil {
		return err
	}
	if d.SeriesName != "" {
		_ = s.repo.SetSeriesRef(ctx, b.ID, d.SeriesName, d.SeriesPosition, d.SeriesKey)
	}
	s.repo.AddEvent(ctx, b.ID, "upgraded", "Re-matched to Hardcover ("+d.Key+")")
	return nil
}
