package automation

import (
	"context"
	"io"
	"log/slog"
	"net/http/httptest"
	"sort"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

// bookRSSHarness is a store-backed coordinator whose only indexer's feed is the fake
// Torznab (Recent answers with whatever it offers) and whose download client is the fake
// qBittorrent, both reached over HTTP through the real services.
type bookRSSHarness struct {
	c    *Coordinator
	bk   *books.Service
	ctx  context.Context
	qbit *fakeQbit
	feed *fakeTorznab
}

func newBookRSSHarness(t *testing.T) *bookRSSHarness {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	qb, tz := &fakeQbit{}, &fakeTorznab{}
	qsrv := httptest.NewServer(qb.handler())
	t.Cleanup(qsrv.Close)
	tsrv := httptest.NewServer(tz.handler())
	t.Cleanup(tsrv.Close)

	ix := indexer.NewService(st.DB(), log, "")
	if _, err := ix.Create(ctx, indexer.Indexer{Name: "Fake", Kind: indexer.KindTorznab, URL: tsrv.URL, Priority: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	dl := download.NewService(st.DB(), log)
	if _, err := dl.Create(ctx, download.Client{Name: "qb", Kind: download.KindQbittorrent, URL: qsrv.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	bk := books.NewService(st.DB(), nil, log)
	c := &Coordinator{
		db: st.DB(), log: log, bus: eventbus.New(log), indexers: ix, downloads: dl, books: bk,
		quality: quality.NewService(st.DB()), downloadsDir: t.TempDir(),
	}
	return &bookRSSHarness{c: c, bk: bk, ctx: ctx, qbit: qb, feed: tz}
}

// add puts monitored (or not) books in the library, straight from metadata.
func (h *bookRSSHarness) add(t *testing.T, monitored bool, works ...metadata.BookResult) []books.Book {
	t.Helper()
	added, _ := h.bk.AddWorks(h.ctx, works, "", monitored)
	if len(added) != len(works) {
		t.Fatalf("added %d books, want %d", len(added), len(works))
	}
	return added
}

// grabbed is the releases the client was asked to add, by title.
func (h *bookRSSHarness) grabbed() []string {
	var out []string
	for _, call := range h.qbit.callLog() {
		if !strings.HasPrefix(call, "add ") {
			continue
		}
		url := strings.TrimPrefix(call, "add ")
		for _, title := range h.feed.releases {
			if magnetFor(title) == url {
				out = append(out, title)
			}
		}
	}
	sort.Strings(out)
	return out
}

// BOOK-05: the RSS path filters with the word-boundary matcher the search path uses. The
// old substring test over space-less keys grabbed "The Institute" for "It" and "Dune
// Messiah" for "Dune".
func TestRSSSyncBooksWordBoundary(t *testing.T) {
	h := newBookRSSHarness(t)
	lib := h.add(t, true,
		metadata.BookResult{Key: "OL1W", Title: "It", Author: "Stephen King"},
		metadata.BookResult{Key: "OL2W", Title: "Dune", Author: "Frank Herbert"},
	)
	// The sequel is in the library but not wanted: its release must not land on Dune.
	h.add(t, false, metadata.BookResult{Key: "OL3W", Title: "Dune Messiah", Author: "Frank Herbert"})
	h.feed.offer(
		"Stephen King - The Institute (2019) EPUB",
		"Frank Herbert - Dune Messiah EPUB",
		"Stephen King - It (1986) EPUB",
		"Frank Herbert - Dune (1965) EPUB",
		"Anne Rice - Interview with the Vampire Special Edition EPUB",
	)
	h.c.RSSSyncBooks(h.ctx)

	want := []string{"Frank Herbert - Dune (1965) EPUB", "Stephen King - It (1986) EPUB"}
	if got := h.grabbed(); strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("grabbed %q, want %q", got, want)
	}

	// The grab is on the book's timeline, worded as the search path words it.
	ev, err := h.bk.Events(h.ctx, lib[0].ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, e := range ev {
		if e.Event == "grabbed" && e.Detail == "Grabbed the ebook edition from Fake: Stephen King - It (1986) EPUB" {
			found = true
		}
	}
	if !found {
		t.Errorf("no 'grabbed' timeline event for It: %+v", ev)
	}
}

// An RSS feed is every new upload, so a release for a same-titled book by someone else
// must not be grabbed: when the book has an author, the release has to name them.
func TestRSSSyncBooksNeedsTheAuthor(t *testing.T) {
	h := newBookRSSHarness(t)
	h.add(t, true, metadata.BookResult{Key: "OL1W", Title: "It", Author: "Stephen King"})
	h.feed.offer("Alexa Chung - It (2013) EPUB")
	h.c.RSSSyncBooks(h.ctx)
	if got := h.grabbed(); len(got) != 0 {
		t.Errorf("grabbed %q for Stephen King's It", got)
	}
}

// The profile decides the editions: the default book profile here is ebook-only, so an
// audiobook upload is never grabbed by RSS.
func TestRSSSyncBooksOnlyWantedEditions(t *testing.T) {
	h := newBookRSSHarness(t)
	h.add(t, true, metadata.BookResult{Key: "OL1W", Title: "Project Hail Mary", Author: "Andy Weir"})
	h.feed.offer("Andy Weir - Project Hail Mary (2021) [M4B]")
	h.c.RSSSyncBooks(h.ctx)
	if got := h.grabbed(); len(got) != 0 {
		t.Errorf("an ebook-only book got an audiobook from RSS: %q", got)
	}
}

// The in-flight check resolves each queue item once against one snapshot, instead of a
// books-table read per queue item per book.
func TestBooksDownloadingMatchesEachItemOnce(t *testing.T) {
	lib := []books.Book{
		{ID: 1, Title: "Dune", Author: "Frank Herbert"},
		{ID: 2, Title: "It", Author: "Stephen King"},
		{ID: 3, Title: "The Stand", Author: "Stephen King"},
	}
	calls := 0
	base := books.MatcherOver(lib)
	match := func(name string) (books.Book, bool) { calls++; return base(name) }
	queue := []download.Item{
		{Name: "Frank Herbert - Dune (1965) EPUB", Category: bookCategory},
		{Name: "Some.Movie.2020.1080p", Category: "arrmada"},
		{Name: "Stephen King - It (1986) M4B", Category: bookCategory},
	}
	got := booksDownloading(queue, match)
	if calls != 2 {
		t.Errorf("matched %d times, want once per book-category item (2)", calls)
	}
	if !got[1] || !got[2] || got[3] {
		t.Errorf("downloading = %v, want Dune and It only", got)
	}
}

// BOOK-07: MyAnonaMouse's uploads now share the RSS feed, so the movie and series sweeps
// drop book uploads before matching — a book titled like a film is never grabbed as it.
func TestWithoutBookUploads(t *testing.T) {
	feed := []indexer.Release{
		{Title: "Dune [M4B]", Format: "M4B", Indexer: "MyAnonaMouse"},
		{Title: "Dune.2021.2160p.WEB-DL.DDP5.1.HEVC-GRP", Indexer: "Fake"},
	}
	got := withoutBookUploads(feed)
	if len(got) != 1 || got[0].Indexer != "Fake" {
		t.Errorf("kept %q, want only the video release", titles(got))
	}
}
