package automation

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/jobs"
)

// The scheduled missing-books sweep gives a book two tries and then leaves it to the
// Search button on its page, which is the right default for a book nobody carries. A
// library catalogued from disk is different: hundreds of books that each have one
// edition, never searched at all, or searched twice years before the other edition
// was ever uploaded. This is the manual sweep for that case — every book, monitored
// or not, that lacks an edition its profile wants, back-off ignored, one after another with a
// pause between so the indexers aren't hammered. It only ever fills a gap: a book
// with both editions is not touched, and nothing on disk is replaced.

// BookSweepStatus is the manual sweep's progress, for the Books page.
type BookSweepStatus struct {
	Running   bool     `json:"running"`
	Total     int      `json:"total"`   // books with a wanted edition missing
	Done      int      `json:"done"`    // searched so far
	Grabbed   int      `json:"grabbed"` // editions sent to the download client
	Skipped   int      `json:"skipped"` // already downloading
	StartedAt int64    `json:"started_at,omitempty"`
	EndedAt   int64    `json:"ended_at,omitempty"`
	Notes     []string `json:"notes,omitempty"` // the first few books nothing was found for
}

const (
	bookSweepPause    = 3 * time.Second // between books: one search per wanted edition, across every indexer
	bookSweepMaxNotes = 12
)

// outageNote words an indexer outage for the sweep's notes.
func outageNote(err error) string {
	if errors.Is(err, indexer.ErrNoIndexers) {
		return "no enabled indexer serves books"
	}
	return fmt.Sprintf("every indexer failed (%v)", err)
}

// BookSweepStatus reports the manual sweep's state.
func (c *Coordinator) BookSweepStatus() BookSweepStatus {
	c.bookSweepMu.Lock()
	defer c.bookSweepMu.Unlock()
	st := c.bookSweep
	st.Notes = append([]string(nil), st.Notes...)
	return st
}

// BeginBookSweep claims the manual sweep; false when one is running. The caller runs it
// with RunBookSweep, or gives the claim back with AbandonBookSweep. Claimed before the job
// starts so the Books page's first status read already says it is running.
func (c *Coordinator) BeginBookSweep() bool {
	c.bookSweepMu.Lock()
	defer c.bookSweepMu.Unlock()
	if c.bookSweep.Running {
		return false
	}
	c.bookSweep = BookSweepStatus{Running: true, StartedAt: time.Now().Unix()}
	return true
}

// AbandonBookSweep releases a claim that never ran.
func (c *Coordinator) AbandonBookSweep() {
	c.bookSweepMu.Lock()
	c.bookSweep.Running, c.bookSweep.EndedAt = false, time.Now().Unix()
	c.bookSweepMu.Unlock()
}

// RunBookSweep runs a claimed sweep to the end and returns its final status.
func (c *Coordinator) RunBookSweep(ctx context.Context) BookSweepStatus {
	c.runBookSweep(WithDefaultSearchTrigger(ctx, TriggerManual)) // a person asked for it
	return c.BookSweepStatus()
}

// SubmitBookSweep claims the sweep and starts it as a books.search-missing job. started is
// false when one is already running.
func (c *Coordinator) SubmitBookSweep(ctx context.Context, sub jobs.Submitter, trigger string) (jobID int64, started bool, err error) {
	if !c.BeginBookSweep() {
		return 0, false, nil
	}
	id, existing, err := jobs.Start(ctx, sub, c.log, jobs.Spec{
		// Its own class: the sweep paces itself (one book every few seconds), and queued
		// behind two long searches its status would read "not running" to the page.
		Kind: "books.search-missing", Target: "all", Trigger: trigger, Class: "books.search-missing", Abandon: c.AbandonBookSweep,
		Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
			st := c.RunBookSweep(ctx)
			p.SetMessage(fmt.Sprintf("Searched %d books: %d editions grabbed", st.Done, st.Grabbed))
			st.Notes = nil // the page reads these from the status endpoint
			return st, nil
		},
	})
	if err != nil || existing {
		c.AbandonBookSweep()
		return id, false, err
	}
	return id, true, nil
}

// MissingBookEditions counts the books the sweep would search: every book lacking an
// edition its profile wants. Monitoring is left out on purpose — it governs the
// automatic sweep, and a library catalogued from disk is mostly unmonitored; this
// sweep is the user asking.
func (c *Coordinator) MissingBookEditions(ctx context.Context) int {
	return len(c.bookSweepTargets(ctx))
}

func (c *Coordinator) bookSweepTargets(ctx context.Context) []books.Book {
	if c.books == nil {
		return nil
	}
	all, err := c.books.List(ctx)
	if err != nil {
		return nil
	}
	var out []books.Book
	for _, b := range all {
		sp := c.bookProfile(ctx, b.QualityProfile)
		wantEbook, wantAudio := books.WantedEditions(sp.FormatScores)
		if (wantEbook && b.Ebook == nil) || (wantAudio && b.Audiobook == nil) {
			out = append(out, b)
		}
	}
	return out
}

func (c *Coordinator) runBookSweep(ctx context.Context) {
	set := func(fn func(*BookSweepStatus)) {
		c.bookSweepMu.Lock()
		fn(&c.bookSweep)
		c.bookSweepMu.Unlock()
	}
	defer set(func(st *BookSweepStatus) { st.Running = false; st.EndedAt = time.Now().Unix() })

	targets := c.bookSweepTargets(ctx)
	set(func(st *BookSweepStatus) { st.Total = len(targets) })
	queue, qerr := c.downloads.Queue(ctx)
	if qerr != nil {
		c.log.Warn("book sweep: couldn't read the download queue — treating nothing as in flight", "err", qerr)
	}
	var downloading map[int64]bool
	if qerr == nil && len(targets) > 0 { // no targets also covers a nil books service
		downloading = booksDownloading(queue, c.books.Matcher(ctx))
	}
	// Two outages in a row end the run, the same as the scheduled sweeps: walking the rest
	// would only hit the dead indexers once per book and fill the notes with copies of one
	// error, when what the user needs to hear is "the indexers are down".
	var outage outageTally
	for i, b := range targets {
		if ctx.Err() != nil {
			return
		}
		if downloading[b.ID] {
			set(func(st *BookSweepStatus) { st.Skipped++; st.Done++ })
			continue
		}
		out, err := c.searchBookOnce(ctx, b.ID)
		n := out.Grabbed
		if errors.Is(err, ErrAlreadySearching) {
			set(func(st *BookSweepStatus) { st.Skipped++; st.Done++ })
			continue
		}
		if outage.note(err) && outage.stop() {
			set(func(st *BookSweepStatus) {
				st.Done++
				st.Notes = append(st.Notes, fmt.Sprintf("Stopped: %s", outageNote(err)))
			})
			c.log.Warn("book sweep: stopped — no indexer could answer", "searched", i+1, "of", len(targets), "err", err)
			return
		}
		set(func(st *BookSweepStatus) {
			st.Done++
			st.Grabbed += n
			switch {
			case err != nil && len(st.Notes) < bookSweepMaxNotes:
				st.Notes = append(st.Notes, fmt.Sprintf("%s — search failed: %v", b.Title, err))
			case err == nil && n == 0 && len(st.Notes) < bookSweepMaxNotes:
				st.Notes = append(st.Notes, b.Title+" — nothing found")
			}
		})
		if i < len(targets)-1 {
			select {
			case <-ctx.Done():
				return
			case <-time.After(bookSweepPause):
			}
		}
	}
	st := c.BookSweepStatus()
	c.log.Info("book sweep: finished", "searched", st.Done, "grabbed", st.Grabbed, "skipped_downloading", st.Skipped)
}
