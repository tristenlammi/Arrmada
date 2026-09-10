package automation

import (
	"context"
	"fmt"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
)

// The scheduled missing-books sweep gives a book two tries and then leaves it to the
// Search button on its page, which is the right default for a book nobody carries. A
// library catalogued from disk is different: hundreds of books that each have one
// edition, never searched at all, or searched twice years before the other edition
// was ever uploaded. This is the manual sweep for that case — every monitored book
// that lacks an edition its profile wants, back-off ignored, one after another with a
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

// BookSweepStatus reports the manual sweep's state.
func (c *Coordinator) BookSweepStatus() BookSweepStatus {
	c.bookSweepMu.Lock()
	defer c.bookSweepMu.Unlock()
	st := c.bookSweep
	st.Notes = append([]string(nil), st.Notes...)
	return st
}

// StartBookSweep begins the manual sweep in the background; false when one is running.
func (c *Coordinator) StartBookSweep(ctx context.Context) bool {
	c.bookSweepMu.Lock()
	if c.bookSweep.Running {
		c.bookSweepMu.Unlock()
		return false
	}
	c.bookSweep = BookSweepStatus{Running: true, StartedAt: time.Now().Unix()}
	c.bookSweepMu.Unlock()
	go c.runBookSweep(ctx)
	return true
}

// MissingBookEditions counts the books the sweep would search: monitored, and lacking
// an edition their profile wants.
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
		if !b.Monitored {
			continue
		}
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
	for i, b := range targets {
		if ctx.Err() != nil {
			return
		}
		if qerr == nil && c.bookDownloading(ctx, queue, b.ID) {
			set(func(st *BookSweepStatus) { st.Skipped++; st.Done++ })
			continue
		}
		n, err := c.searchBookOnce(ctx, b.ID)
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
