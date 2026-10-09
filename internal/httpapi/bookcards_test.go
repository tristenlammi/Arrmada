package httpapi

import (
	"context"
	"testing"

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
