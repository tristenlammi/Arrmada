package books

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// seriesCatalogue answers one series lookup with a canned list.
type seriesCatalogue struct {
	stubCatalogue
	series *metadata.SeriesInfo
}

func (c *seriesCatalogue) SeriesBooks(context.Context, string) (*metadata.SeriesInfo, error) {
	return c.series, nil
}

// A same-author book that shares a title prefix with one in the library is a new book:
// Add creates it, and an author's catalogue adds both siblings (still deduping a
// catalogue that lists one novel twice).
func TestAddKeepsPrefixSiblingsApart(t *testing.T) {
	repo, ctx := historyRepo(t)
	s := &Service{repo: repo, meta: &stubCatalogue{}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	thrawn, err := s.Add(ctx, "hc:1", "", true, metadata.BookResult{Title: "Thrawn", Author: "Timothy Zahn"})
	if err != nil {
		t.Fatal(err)
	}
	alliances, err := s.Add(ctx, "hc:2", "", true, metadata.BookResult{Title: "Thrawn: Alliances", Author: "Timothy Zahn"})
	if err != nil {
		t.Fatalf("a prefix sibling was refused: %v", err)
	}
	if alliances.ID == thrawn.ID {
		t.Fatal("the sibling landed on the existing row")
	}
	// The same book under another key is still the existing row.
	if b, err := s.Add(ctx, "OL9W", "", true, metadata.BookResult{Title: "Thrawn (Star Wars)", Author: "Zahn, Timothy"}); !errors.Is(err, ErrExists) || b.ID != thrawn.ID {
		t.Errorf("a re-listing of Thrawn: id=%d err=%v", b.ID, err)
	}

	added, skipped := s.AddWorks(ctx, []metadata.BookResult{
		{Key: "hc:3", Title: "Thrawn: Treason", Author: "Timothy Zahn"},
		{Key: "hc:4", Title: "Thrawn: Alliances", Author: "Timothy Zahn"}, // already in
		{Key: "hc:5", Title: "Heir to the Empire: Star Wars", Author: "Timothy Zahn"},
		{Key: "hc:6", Title: "Dark Force Rising: Star Wars", Author: "Timothy Zahn"},
		{Key: "hc:7", Title: "Thrawn: Treason (Star Wars)", Author: "Timothy Zahn"}, // listed twice
	}, "", true)
	if len(added) != 3 || skipped != 2 {
		titles := []string{}
		for _, b := range added {
			titles = append(titles, b.Title)
		}
		t.Errorf("added %v, skipped %d; want Treason, Heir and Dark Force Rising added, 2 skipped", titles, skipped)
	}
}

// The Hardcover re-match never moves a library "Thrawn" onto "Thrawn: Alliances".
func TestMatchUpgradeSkipsPrefixSibling(t *testing.T) {
	book := Book{Title: "Thrawn", Author: "Timothy Zahn"}
	results := []metadata.BookResult{
		{Key: "hc:a", Title: "Thrawn: Alliances", Author: "Timothy Zahn"},
		{Key: "hc:t", Title: "Thrawn", Author: "Timothy Zahn"},
	}
	if m := matchUpgrade(book, results); m == nil || m.Key != "hc:t" {
		t.Errorf("matched %+v, want hc:t", m)
	}
	if m := matchUpgrade(book, results[:1]); m != nil {
		t.Errorf("matched the sibling %+v", m)
	}
}

// A series panel lists a sibling of an owned book as missing.
func TestSeriesGapsKeepSiblingsApart(t *testing.T) {
	repo, ctx := historyRepo(t)
	cat := &seriesCatalogue{series: &metadata.SeriesInfo{Key: "hcs:1", Name: "Mistborn", Count: 2, Books: []metadata.BookResult{
		{Key: "hc:10", Title: "Mistborn: The Final Empire", Author: "Brandon Sanderson", SeriesPosition: 1},
		{Key: "hc:11", Title: "Mistborn: Secret History", Author: "Brandon Sanderson", SeriesPosition: 3.5},
	}}}
	s := &Service{repo: repo, meta: cat, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	// Owned under an Open Library key, so only the title can match it.
	owned, err := repo.Create(ctx, Book{OLKey: "OL1W", Title: "The Final Empire", Author: "Brandon Sanderson"})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSeriesRef(ctx, owned.ID, "Mistborn", 1, "hcs:1"); err != nil {
		t.Fatal(err)
	}
	view, missing, ok := s.catalogueSeries(ctx, owned.ID)
	if !ok {
		t.Fatal("no series view")
	}
	if view.Gaps != 1 || len(missing) != 1 || missing[0].Key != "hc:11" {
		t.Errorf("gaps=%d missing=%+v, want only Secret History", view.Gaps, missing)
	}
	if view.Entries[0].BookID != owned.ID {
		t.Errorf("The Final Empire not matched to the owned row: %+v", view.Entries[0])
	}
}
