package automation

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/connstatus"
	"github.com/tristenlammi/arrmada/internal/indexer"
)

// IndexerIssue is one indexer that couldn't answer a search: it errored, or (background
// searches only) it was left alone because it is backing off after repeated failures.
// Before this every search modal said "No releases found on your indexers" whether the
// trackers had nothing or were dead, and the per-indexer errors only reached the log.
type IndexerIssue struct {
	Indexer string `json:"indexer"`
	Error   string `json:"error"`
	Skipped bool   `json:"skipped,omitempty"` // paused, not asked; Error says until when
}

// searchNotes collects which indexers failed or were skipped across every query one search
// makes — a series search sends a broad query, one per season and one per alias; a book
// search tries author+title, then the title alone. It rides on the context so those paths
// don't each have to thread an errors map back up. Safe for concurrent use.
type searchNotes struct {
	mu      sync.Mutex
	errors  map[string]string // indexer -> its first error, redacted
	skipped map[string]string // indexer -> why it was paused
	asked   int               // the most indexers any one query went to
	ran     bool              // at least one indexer query was made

	// What became of each distinct release the search looked at, for a recorded attempt
	// (searchlog.go). Empty for an interactive list, which shows every release instead.
	started  time.Time
	seen     map[string]bool   // release titles considered
	class    map[string]string // release title -> what happened to it (reject code)
	examples map[string]string // reject code -> the first release it applied to
	took     []string          // releases grabbed, in order
}

type searchNotesKey struct{}

// withSearchNotes starts collecting indexer issues for the searches made under the
// returned context. A context that already collects keeps its collector, so an outer
// search (a recorded attempt) sees what an inner one (its grab pass) ran into.
func withSearchNotes(ctx context.Context) (context.Context, *searchNotes) {
	if n := notesFrom(ctx); n != nil {
		return ctx, n
	}
	return newSearchNotes(ctx)
}

// newSearchNotes always starts a fresh collector: one recorded attempt per title search,
// whatever the caller's context carried.
func newSearchNotes(ctx context.Context) (context.Context, *searchNotes) {
	n := &searchNotes{started: time.Now()}
	return context.WithValue(ctx, searchNotesKey{}, n), n
}

// notesFrom is the collector on ctx, or nil (every method is nil-safe).
func notesFrom(ctx context.Context) *searchNotes {
	n, _ := ctx.Value(searchNotesKey{}).(*searchNotes)
	return n
}

// note records one indexer query's result. The per-indexer errors are on the result even
// when the search as a whole failed (AllFailedError comes back with the partial result),
// and on the error itself as well; both are read so a caller that dropped the result
// still counts.
func (n *searchNotes) note(res indexer.SearchResult, err error) {
	if n == nil {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	n.ran = true
	add := func(dst *map[string]string, src map[string]string) {
		for name, msg := range src {
			if *dst == nil {
				*dst = map[string]string{}
			}
			if _, seen := (*dst)[name]; !seen {
				(*dst)[name] = connstatus.Redact(msg)
			}
		}
	}
	add(&n.errors, res.Errors)
	add(&n.skipped, res.Skipped)
	var all *indexer.AllFailedError
	if errors.As(err, &all) {
		add(&n.errors, all.Errors)
		add(&n.skipped, all.Skipped)
	}
	if res.Asked > n.asked {
		n.asked = res.Asked
	}
}

// issues is everything collected, one line per indexer in name order. An indexer that
// both failed one query and was paused for another reads as failed: that's the news.
func (n *searchNotes) issues() []IndexerIssue {
	if n == nil {
		return nil
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return issuesFrom(n.errors, n.skipped)
}

// errorMap is a copy of the failed indexers' (redacted) errors, skipped ones included and
// marked so, for a stored search attempt.
func (n *searchNotes) errorMap() map[string]string {
	out := map[string]string{}
	for _, is := range n.issues() {
		out[is.Indexer] = is.Error
	}
	return out
}

// searched is how many indexers were actually asked by the widest query.
func (n *searchNotes) searched() int {
	if n == nil {
		return 0
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.asked
}

// issuesFrom merges per-indexer errors and pauses into one sorted list. Every message is
// redacted again on the way out: a native searcher's error can carry a URL with a
// passkey or a MAM token in it, and these strings reach the browser.
func issuesFrom(errs, skipped map[string]string) []IndexerIssue {
	out := make([]IndexerIssue, 0, len(errs)+len(skipped))
	for name, msg := range errs {
		out = append(out, IndexerIssue{Indexer: name, Error: connstatus.Redact(msg)})
	}
	for name, msg := range skipped {
		if _, failed := errs[name]; failed {
			continue
		}
		out = append(out, IndexerIssue{Indexer: name, Error: connstatus.Redact(msg), Skipped: true})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Indexer < out[j].Indexer })
	if len(out) == 0 {
		return nil
	}
	return out
}

// search is c.indexers.Search that also tells the context's collector (if any) which
// indexers failed. Every indexer query in this package goes through it.
func (c *Coordinator) search(ctx context.Context, q indexer.SearchQuery) (indexer.SearchResult, error) {
	res, err := c.indexers.Search(ctx, q)
	notesFrom(ctx).note(res, err)
	return res, err
}

// allIndexersFailed reports whether err is a search nobody could answer because every
// indexer errored or is paused — as opposed to there being no indexer for it at all. An
// interactive search shows that as issues on an empty list, not as an error.
func allIndexersFailed(err error) bool {
	var all *indexer.AllFailedError
	return errors.As(err, &all)
}

// withIssues fills in a release list's indexer issues from the search's collector.
func (l ReleaseList) withIssues(n *searchNotes) ReleaseList {
	l.IndexerIssues = n.issues()
	l.Searched = n.searched()
	if l.Releases == nil {
		l.Releases = []RankedRelease{}
	}
	return l
}
