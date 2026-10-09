package requests

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

// catalogue answers GetBook from a fixed set of books, by key.
type catalogue struct {
	metadata.BookProvider
	byKey map[string]metadata.BookResult
}

func (c catalogue) GetBook(_ context.Context, key string) (*metadata.BookDetails, error) {
	return &metadata.BookDetails{BookResult: c.byKey[key]}, nil
}

// bookLinkFixture is a requests service over a fresh store with real books, movies and
// series services (enrichAvailability and the ready sweep read all three).
func bookLinkFixture(t *testing.T, cat catalogue) (*Service, *books.Repo, *sql.DB, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	root := t.TempDir()
	s := &Service{
		repo:    NewRepo(db),
		movies:  movies.NewService(db, nil, nil, root, "", nil, log),
		series:  series.NewService(db, nil, root, log),
		books:   books.NewService(db, cat, log),
		quality: quality.NewService(db),
		log:     log,
	}
	return s, books.NewRepo(db), db, context.Background()
}

func giveEbook(t *testing.T, repo *books.Repo, ctx context.Context, id int64) {
	t.Helper()
	if err := repo.SetEdition(ctx, id, books.KindEbook, "/library/book.epub", "EPUB", 1, 1); err != nil {
		t.Fatal(err)
	}
}

// Approving a request for a book the library holds under another key links the request
// to that row, and starts a search when the row lacks an edition its profile wants.
func TestApproveLinksExistingBookAndSearches(t *testing.T) {
	s, repo, _, ctx := bookLinkFixture(t, catalogue{byKey: map[string]metadata.BookResult{
		"OL1W": {Key: "OL1W", Title: "Dune", Author: "Frank Herbert"},
	}})
	searched := make(chan int64, 1)
	s.searchBook = func(_ context.Context, id int64) (automation.SearchOutcome, error) {
		searched <- id
		return automation.SearchOutcome{}, nil
	}
	held, err := repo.Create(ctx, books.Book{OLKey: "hc:9", Title: "Dune", Author: "Herbert, Frank"})
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.repo.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", Status: StatusPending, RequestedBy: 7})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Approve(ctx, req.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.BookID != held.ID {
		t.Errorf("book_id = %d, want the existing row %d", got.BookID, held.ID)
	}
	select {
	case id := <-searched:
		if id != held.ID {
			t.Errorf("searched book %d, want %d", id, held.ID)
		}
	case <-time.After(5 * time.Second):
		t.Error("no search for an existing book that has no file")
	}

	// Once it has its ebook, a second request for it starts no search.
	giveEbook(t, repo, ctx, held.ID)
	again, err := s.repo.Create(ctx, Request{MediaType: "book", OLKey: "OL2W", Title: "Dune", Author: "Frank Herbert", Status: StatusPending, RequestedBy: 8})
	if err != nil {
		t.Fatal(err)
	}
	s.books = books.NewService(s.repo.db, catalogue{byKey: map[string]metadata.BookResult{
		"OL2W": {Key: "OL2W", Title: "Dune", Author: "Frank Herbert"},
	}}, s.log)
	if _, err := s.Approve(ctx, again.ID, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-searched:
		t.Errorf("searched book %d that already has its edition", id)
	case <-time.After(200 * time.Millisecond):
	}
}

// An existing book that is already downloading isn't searched again on approve: a
// second grab would only duplicate the one in flight.
func TestApproveSkipsSearchWhileDownloading(t *testing.T) {
	s, repo, db, ctx := bookLinkFixture(t, catalogue{byKey: map[string]metadata.BookResult{
		"OL1W": {Key: "OL1W", Title: "Dune", Author: "Frank Herbert"},
	}})
	searched := make(chan int64, 1)
	s.searchBook = func(_ context.Context, id int64) (automation.SearchOutcome, error) {
		searched <- id
		return automation.SearchOutcome{}, nil
	}
	held, err := repo.Create(ctx, books.Book{OLKey: "hc:9", Title: "Dune", Author: "Frank Herbert"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO grabs (movie_id, title, media_type, status) VALUES (?, 'Dune EPUB', 'book', 'grabbed')`, held.ID); err != nil {
		t.Fatal(err)
	}
	req, err := s.repo.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", Status: StatusPending, RequestedBy: 7})
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Approve(ctx, req.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.BookID != held.ID {
		t.Errorf("book_id = %d, want %d", got.BookID, held.ID)
	}
	select {
	case id := <-searched:
		t.Errorf("searched book %d while its download is in flight", id)
	case <-time.After(200 * time.Millisecond):
	}
}

// A request made under an Open Library key still resolves once its book is re-matched
// to a Hardcover key: Available, and "ready" sent exactly once.
func TestLinkedRequestSurvivesRematch(t *testing.T) {
	s, repo, _, ctx := bookLinkFixture(t, catalogue{byKey: map[string]metadata.BookResult{
		"hc:42": {Key: "hc:42", Title: "Dune", Author: "Frank Herbert"},
	}})
	b, err := repo.Create(ctx, books.Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert"})
	if err != nil {
		t.Fatal(err)
	}
	req, err := s.repo.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert",
		Status: StatusApproved, RequestedBy: 7, BookID: b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.repo.db.ExecContext(ctx, `INSERT INTO request_subscribers (request_id, user_id, user_name) VALUES (?, 8, 'bob')`, req.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.books.Rematch(ctx, b.ID, "hc:42", metadata.BookResult{}); err != nil {
		t.Fatal(err)
	}
	giveEbook(t, repo, ctx, b.ID)

	list, err := s.List(ctx, "", 0)
	if err != nil || len(list) != 1 {
		t.Fatalf("list: %v %d", err, len(list))
	}
	if !list[0].Available || list[0].libID != b.ID {
		t.Errorf("available=%v libID=%d, want available on book %d", list[0].Available, list[0].libID, b.ID)
	}

	s.notifyBookRequesters(ctx, b.ID, "hc:42") // the book.imported event
	if err := s.SweepReadyRequests(ctx); err != nil {
		t.Fatal(err)
	}
	s.notifyBookRequesters(ctx, b.ID, "hc:42")
	for _, uid := range []int64{7, 8} {
		inbox, err := s.repo.listUserNotifications(ctx, uid)
		if err != nil {
			t.Fatal(err)
		}
		if len(inbox) != 1 {
			t.Errorf("user %d has %d notifications, want exactly one", uid, len(inbox))
		}
	}

	// Deleting the book unlinks the request rather than failing or removing it.
	if _, err := s.repo.db.ExecContext(ctx, `DELETE FROM books WHERE id = ?`, b.ID); err != nil {
		t.Fatalf("delete book: %v", err)
	}
	after, err := s.repo.Get(ctx, req.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.BookID != 0 {
		t.Errorf("book_id = %d after the book was deleted, want 0", after.BookID)
	}
}

// The boot backfill links a request by key, then by a unique title-and-author match,
// and leaves an ambiguous one alone. A second run changes nothing.
func TestBackfillBookIDs(t *testing.T) {
	s, repo, _, ctx := bookLinkFixture(t, catalogue{})
	byKey, _ := repo.Create(ctx, books.Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert"})
	byTitle, _ := repo.Create(ctx, books.Book{OLKey: "hc:7", Title: "Mistborn: The Final Empire", Author: "Brandon Sanderson"})
	_, _ = repo.Create(ctx, books.Book{OLKey: "hc:8", Title: "Thrawn", Author: "Timothy Zahn"})
	_, _ = repo.Create(ctx, books.Book{OLKey: "OL8W", Title: "Thrawn", Author: "Timothy Zahn"})

	mk := func(key, title, author string) int64 {
		r, err := s.repo.Create(ctx, Request{MediaType: "book", OLKey: key, Title: title, Author: author, Status: StatusApproved, RequestedBy: 7})
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	rKey := mk("OL1W", "Dune", "Frank Herbert")
	rTitle := mk("OL2W", "The Final Empire", "Brandon Sanderson")
	rAmbiguous := mk("OL3W", "Thrawn", "Timothy Zahn")
	rSibling := mk("OL4W", "Thrawn: Alliances", "Timothy Zahn")

	linked, ambiguous, err := s.BackfillBookIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if linked != 2 || ambiguous != 1 {
		t.Errorf("linked=%d ambiguous=%d, want 2 and 1", linked, ambiguous)
	}
	for id, want := range map[int64]int64{rKey: byKey.ID, rTitle: byTitle.ID, rAmbiguous: 0, rSibling: 0} {
		got, _ := s.repo.Get(ctx, id)
		if got.BookID != want {
			t.Errorf("request %d (%s) book_id = %d, want %d", id, got.Title, got.BookID, want)
		}
	}
	if linked, _, _ := s.BackfillBookIDs(ctx); linked != 0 {
		t.Errorf("second run linked %d", linked)
	}
}

// After the Hardcover upgrade a book has two keys. Requests from a card under either one
// are one request: the second requester is subscribed to the first.
func TestCreateWithAliasKeyAttachesToExistingRequest(t *testing.T) {
	s, repo, _, ctx := bookLinkFixture(t, catalogue{byKey: map[string]metadata.BookResult{
		"hc:42": {Key: "hc:42", Title: "Dune", Author: "Frank Herbert"},
	}})
	b, err := repo.Create(ctx, books.Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.books.Rematch(ctx, b.ID, "hc:42", metadata.BookResult{}); err != nil {
		t.Fatal(err)
	}
	first, sub, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert",
		RequestedBy: 7, RequestedByName: "alice"}, false)
	if err != nil || sub {
		t.Fatalf("first request: %v subscribed=%v", err, sub)
	}
	if first.BookID != b.ID {
		t.Errorf("first request book_id = %d, want %d (its key is the book's former key)", first.BookID, b.ID)
	}
	second, sub, err := s.Create(ctx, Request{MediaType: "book", OLKey: "hc:42", Title: "Dune", Author: "Frank Herbert",
		RequestedBy: 8, RequestedByName: "bob"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !sub || second.ID != first.ID {
		t.Errorf("second request = id %d subscribed=%v, want subscribed to %d", second.ID, sub, first.ID)
	}
	subs, _ := s.repo.Subscribers(ctx, first.ID)
	if len(subs) != 1 || subs[0].UserID != 8 {
		t.Errorf("subscribers = %+v, want bob", subs)
	}
}

// A book not in the library yet, requested from an Open Library card and a Hardcover
// card, is one request too: same title and author.
func TestCreateSameBookOtherCatalogueAttaches(t *testing.T) {
	s, _, _, ctx := bookLinkFixture(t, catalogue{})
	first, _, err := s.Create(ctx, Request{MediaType: "book", OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", RequestedBy: 7}, false)
	if err != nil {
		t.Fatal(err)
	}
	second, sub, err := s.Create(ctx, Request{MediaType: "book", OLKey: "hc:42", Title: "Dune", Author: "Herbert, Frank", RequestedBy: 8}, false)
	if err != nil {
		t.Fatal(err)
	}
	if !sub || second.ID != first.ID {
		t.Errorf("second = %d subscribed=%v, want subscribed to %d", second.ID, sub, first.ID)
	}
	// A prefix sibling is another book.
	third, sub, err := s.Create(ctx, Request{MediaType: "book", OLKey: "hc:43", Title: "Dune Messiah", Author: "Frank Herbert", RequestedBy: 8}, false)
	if err != nil {
		t.Fatal(err)
	}
	if sub || third.ID == first.ID {
		t.Error("Dune Messiah was folded into the Dune request")
	}
}

// The backfill records the key a request was made under as one of its book's keys.
func TestBackfillAddsRequestKey(t *testing.T) {
	s, repo, _, ctx := bookLinkFixture(t, catalogue{})
	b, _ := repo.Create(ctx, books.Book{OLKey: "hc:7", Title: "Mistborn: The Final Empire", Author: "Brandon Sanderson"})
	if _, err := s.repo.Create(ctx, Request{MediaType: "book", OLKey: "OL2W", Title: "The Final Empire", Author: "Brandon Sanderson", Status: StatusApproved, RequestedBy: 7}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := s.BackfillBookIDs(ctx); err != nil {
		t.Fatal(err)
	}
	if id, ok := s.books.BookIDForKey(ctx, "OL2W"); !ok || id != b.ID {
		t.Errorf("OL2W resolves to %d %v, want %d", id, ok, b.ID)
	}
}
