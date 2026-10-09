package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/series"
)

// bookCardServer is a route server with real books and requests services over its store.
func bookCardServer(t *testing.T) *routeServer {
	t.Helper()
	root := t.TempDir()
	return newRouteServer(t, func(d *Deps) {
		db := d.Store.DB()
		d.Books = books.NewService(db, nil, d.Log)
		mv := movies.NewService(db, nil, nil, root, "", nil, d.Log)
		sr := series.NewService(db, nil, root, d.Log)
		d.Requests = requests.NewService(db, mv, sr, d.Books, nil, nil, nil, "", d.Log)
	})
}

// A fulfilled request whose book was re-matched to a new catalogue key sits on the shelf
// tagged Mine, not under "Your requests"; its Discover card under the new key reads as
// requested.
func TestMyBooksFollowsLinkedRequest(t *testing.T) {
	s := bookCardServer(t)
	ctx := context.Background()
	u, cookie := s.user(t, "reader@example.com", auth.RoleRequester)
	repo := books.NewRepo(s.st.DB())
	b, err := repo.Create(ctx, books.Book{OLKey: "hc:42", Title: "Dune", Author: "Frank Herbert"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetEdition(ctx, b.ID, books.KindEbook, "/library/dune.epub", "EPUB", 1, 1); err != nil {
		t.Fatal(err)
	}
	// Made under the Open Library key the book had before the re-match.
	if _, err := s.st.DB().Exec(`INSERT INTO requests (media_type, ol_key, title, author, status, requested_by, book_id)
		VALUES ('book', 'OL1W', 'Dune', 'Frank Herbert', 'approved', ?, ?)`, u.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	rec := s.do("GET", "/api/v1/me/books", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Books    []MyBook    `json:"books"`
		Requests []MyRequest `json:"requests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Books) != 1 || !got.Books[0].Mine {
		t.Errorf("shelf = %+v, want Dune tagged Mine", got.Books)
	}
	if len(got.Requests) != 0 {
		t.Errorf("a fulfilled request is still listed: %+v", got.Requests)
	}
	a := &api{deps: s.deps}
	cards := a.enrichBookCards(ctx, []metadata.BookResult{{Key: "hc:42", Title: "Dune", Author: "Frank Herbert"}})
	if cards[0].RequestStatus != "approved" {
		t.Errorf("card under the new key: status %q, want approved", cards[0].RequestStatus)
	}
}

// A Discover card for a book that only shares a title prefix with one in the library
// is not "in your library", so it keeps its Request button; the same book under another
// catalogue's key still is.
func TestBookCardsKeepPrefixSiblingsRequestable(t *testing.T) {
	s := bookCardServer(t)
	ctx := context.Background()
	if _, err := books.NewRepo(s.st.DB()).Create(ctx, books.Book{OLKey: "OL1W", Title: "Thrawn", Author: "Timothy Zahn"}); err != nil {
		t.Fatal(err)
	}
	a := &api{deps: s.deps}
	cards := a.enrichBookCards(ctx, []metadata.BookResult{
		{Key: "hc:2", Title: "Thrawn: Alliances", Author: "Timothy Zahn"},
		{Key: "hc:1", Title: "Thrawn (Star Wars)", Author: "Zahn, Timothy"},
	})
	if cards[0].InLibrary {
		t.Error("Thrawn: Alliances reads as owned because Thrawn is")
	}
	if !cards[1].InLibrary {
		t.Error("Thrawn under a Hardcover key should read as owned")
	}
}

// POST /requests takes a book request's Read / Listen / Both choice, returns it on the
// request, and refuses any other value.
func TestCreateBookRequestFormats(t *testing.T) {
	s := bookCardServer(t)
	_, cookie := s.user(t, "reader@example.com", auth.RoleRequester)
	rec := s.doJSON("POST", "/api/v1/requests", cookie, `{"media_type":"book","ol_key":"OL1W","title":"Dune","author":"Frank Herbert","formats":"paperback"}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("formats=paperback: HTTP %d, want 400", rec.Code)
	}
	rec = s.doJSON("POST", "/api/v1/requests", cookie, `{"media_type":"book","ol_key":"OL1W","title":"Dune","author":"Frank Herbert","formats":"audiobook"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Request requests.Request `json:"request"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Request.Formats != requests.FormatsAudiobook {
		t.Errorf("formats = %q, want audiobook", got.Request.Formats)
	}
}

// A "both" request whose ebook is here stays on the requester's list, saying the
// audiobook is still on the way; the book is on the shelf meanwhile.
func TestMyBooksShowsFormatStillComing(t *testing.T) {
	s := bookCardServer(t)
	ctx := context.Background()
	u, cookie := s.user(t, "reader@example.com", auth.RoleRequester)
	repo := books.NewRepo(s.st.DB())
	b, err := repo.Create(ctx, books.Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetEdition(ctx, b.ID, books.KindEbook, "/library/dune.epub", "EPUB", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.DB().Exec(`INSERT INTO requests (media_type, ol_key, title, author, status, requested_by, book_id, formats)
		VALUES ('book', 'OL1W', 'Dune', 'Frank Herbert', 'approved', ?, ?, 'both')`, u.ID, b.ID); err != nil {
		t.Fatal(err)
	}
	rec := s.do("GET", "/api/v1/me/books", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Books    []MyBook    `json:"books"`
		Requests []MyRequest `json:"requests"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Books) != 1 {
		t.Errorf("shelf = %+v, want Dune", got.Books)
	}
	if len(got.Requests) != 1 || got.Requests[0].Waiting != requests.FormatsAudiobook || got.Requests[0].Formats != requests.FormatsBoth {
		t.Errorf("requests = %+v, want Dune with the audiobook still coming", got.Requests)
	}
}

// A card for a book held as an ebook says so per format, with what its profile wants
// and what the request on it asked for, so the page can offer "Request audiobook".
func TestBookCardsPerFormat(t *testing.T) {
	s := bookCardServer(t)
	ctx := context.Background()
	repo := books.NewRepo(s.st.DB())
	b, err := repo.Create(ctx, books.Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetEdition(ctx, b.ID, books.KindEbook, "/library/dune.epub", "EPUB", 1, 1); err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.DB().Exec(`INSERT INTO requests (media_type, ol_key, title, author, status, requested_by, book_id, formats)
		VALUES ('book', 'OL1W', 'Dune', 'Frank Herbert', 'pending', 1, ?, 'audiobook')`, b.ID); err != nil {
		t.Fatal(err)
	}
	a := &api{deps: s.deps}
	cards := a.enrichBookCards(ctx, []metadata.BookResult{
		{Key: "OL1W", Title: "Dune", Author: "Frank Herbert"},
		{Key: "OL2W", Title: "Neuromancer", Author: "William Gibson"},
	})
	c := cards[0]
	if !c.HasEbook || c.HasAudiobook || !c.WantEbook || c.WantAudiobook || c.RequestFormats != "audiobook" || c.RequestStatus != "pending" {
		t.Errorf("Dune card = %+v, want ebook here, ebook wanted, audiobook requested", c)
	}
	if o := cards[1]; o.InLibrary || o.HasEbook || o.HasAudiobook || o.WantEbook || o.RequestFormats != "" {
		t.Errorf("a book not in the library = %+v", o)
	}
}

// A card still carrying a book's former key (the Open Library key from before the
// Hardcover upgrade) reads In library even when its title differs from the library's.
func TestBookCardsAliasKeyIsInLibrary(t *testing.T) {
	s := bookCardServer(t)
	ctx := context.Background()
	repo := books.NewRepo(s.st.DB())
	b, err := repo.Create(ctx, books.Book{OLKey: "hc:42", Title: "Mistborn", Author: "Brandon Sanderson"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetEdition(ctx, b.ID, books.KindEbook, "/library/m.epub", "EPUB", 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := repo.AddKey(ctx, "OL1W", b.ID, ""); err != nil {
		t.Fatal(err)
	}
	a := &api{deps: s.deps}
	cards := a.enrichBookCards(ctx, []metadata.BookResult{{Key: "OL1W", Title: "Mistborn (The Final Empire)", Author: "Sanderson"}})
	if !cards[0].InLibrary || !cards[0].HasFile {
		t.Errorf("card under the former key = %+v, want in library with a file", cards[0])
	}

	// The detail page lists the former key, not the current one.
	_, cookie := s.user(t, "boss@example.com", auth.RoleManager)
	rec := s.do("GET", fmt.Sprintf("/api/v1/books/%d", b.ID), cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Aliases []books.BookKey `json:"aliases"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Aliases) != 1 || got.Aliases[0].Key != "OL1W" || got.Aliases[0].Source != books.KeySourceOpenLibrary {
		t.Errorf("aliases = %+v, want just OL1W from openlibrary", got.Aliases)
	}
}
