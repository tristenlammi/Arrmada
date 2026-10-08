package books

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// hardcoverCatalogue answers every Hardcover search with the same results and hands
// back a canned book for GetBook, so the upgrade can be run without the network.
type hardcoverCatalogue struct {
	stubCatalogue
	results []metadata.BookResult
}

func (h *hardcoverCatalogue) Source() string { return metadata.SourceHardcover }
func (h *hardcoverCatalogue) SearchBooksFrom(context.Context, string, string) ([]metadata.BookResult, error) {
	return h.results, nil
}
func (h *hardcoverCatalogue) GetBook(_ context.Context, key string) (*metadata.BookDetails, error) {
	for _, r := range h.results {
		if r.Key == key {
			return &metadata.BookDetails{BookResult: r}, nil
		}
	}
	return nil, errors.New("not found")
}

// upgradeFixture is a library with one row already on Hardcover and one Open Library
// row that Hardcover places on the very same book.
func upgradeFixture(t *testing.T) (*Service, *Repo, context.Context, Book, Book) {
	t.Helper()
	repo, ctx := historyRepo(t)
	cat := &hardcoverCatalogue{results: []metadata.BookResult{
		{Key: "hc:42", Title: "Mistborn: The Final Empire", Author: "Brandon Sanderson"},
	}}
	s := &Service{repo: repo, meta: cat, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	held, err := repo.Create(ctx, Book{OLKey: "hc:42", Title: "Mistborn: The Final Empire", Author: "Brandon Sanderson"})
	if err != nil {
		t.Fatal(err)
	}
	old, err := repo.Create(ctx, Book{OLKey: "OL1W", Title: "Mistborn: The Final Empire", Author: "Sanderson, Brandon"})
	if err != nil {
		t.Fatal(err)
	}
	return s, repo, ctx, held, old
}

// A re-match that lands on a key another row already holds leaves both rows exactly
// as they were and says so on both timelines — it never folds one into the other.
func TestUpgradeFlagsInsteadOfMerging(t *testing.T) {
	s, repo, ctx, held, old := upgradeFixture(t)

	outcome, reason, err := s.upgradeOne(ctx, old)
	if err != nil {
		t.Fatal(err)
	}
	if outcome != "flagged" {
		t.Fatalf("outcome = %q (%s), want flagged", outcome, reason)
	}
	list, _ := repo.List(ctx)
	if len(list) != 2 {
		t.Fatalf("%d rows left, want both: %+v", len(list), list)
	}
	if got, _ := repo.Get(ctx, old.ID); got.OLKey != "OL1W" {
		t.Errorf("the flagged row moved to %q; it must keep its own key", got.OLKey)
	}
	for _, id := range []int64{held.ID, old.ID} {
		evs, _ := repo.Events(ctx, id, 10)
		if len(evs) == 0 || evs[0].Event != "possible_duplicate" {
			t.Errorf("book %d: want a possible_duplicate event, got %+v", id, evs)
		}
	}
}

// The duplicate's audio versions and the family's listening places survive a whole
// upgrade run, and the run reports the row as flagged rather than merged.
func TestUpgradeRunKeepsDuplicateAudioAndListening(t *testing.T) {
	s, repo, ctx, _, old := upgradeFixture(t)
	db := repo.db
	if _, err := repo.createAudioVersion(ctx, old.ID, "GraphicAudio", []string{"graphicaudio"}, true); err != nil {
		t.Fatal(err)
	}
	res, err := db.ExecContext(ctx, `INSERT INTO users (username, password_hash, role) VALUES ('kid', 'x', 'requester')`)
	if err != nil {
		t.Fatal(err)
	}
	uid, _ := res.LastInsertId()
	key := fmt.Sprintf("b%d", old.ID)
	if _, err := db.ExecContext(ctx, `INSERT INTO listen_progress (user_id, item_key, position, updated_at) VALUES (?, ?, 1234, 1)`, uid, key); err != nil {
		t.Fatal(err)
	}

	s.upgrade.status = UpgradeStatus{Running: true}
	s.runUpgrade(ctx)

	st := s.UpgradeStatus()
	if st.Flagged != 1 || st.Upgraded != 0 || st.Unmatched != 0 {
		t.Errorf("status = %+v, want one flagged", st)
	}
	if vs, _ := repo.ListAudioVersions(ctx, old.ID); len(vs) != 1 {
		t.Errorf("audio versions = %d, want the one the duplicate had", len(vs))
	}
	var n int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM listen_progress WHERE item_key = ?`, key).Scan(&n); err != nil || n != 1 {
		t.Errorf("listening place lost: n=%d err=%v", n, err)
	}
	if _, err := repo.Get(ctx, old.ID); err != nil {
		t.Errorf("the duplicate row is gone: %v", err)
	}
}

// The re-match must land on the catalogue's entry for the same book across the ways
// catalogues render it, and must not land on a different book of the same title.
func TestMatchUpgrade(t *testing.T) {
	book := Book{Title: "Dust", Author: "Hugh Howey"}
	results := []metadata.BookResult{
		{Key: "hc:1", Title: "Dust", Author: "Elizabeth Bear"},
		{Key: "hc:2", Title: "Dust (Silo, #3)", Author: "Howey, Hugh"},
		{Key: "hc:3", Title: "Wool", Author: "Hugh Howey"},
	}
	if m := matchUpgrade(book, results); m == nil || m.Key != "hc:2" {
		t.Errorf("matched %+v, want hc:2 (same title, author shares 'howey')", m)
	}
	// Exact key match wins over an overlap.
	exact := append([]metadata.BookResult{{Key: "hc:9", Title: "Dust", Author: "Hugh Howey"}}, results...)
	if m := matchUpgrade(book, exact); m == nil || m.Key != "hc:9" {
		t.Errorf("matched %+v, want the exact hc:9", m)
	}
	// Same title, different author, no overlap: not a match.
	if m := matchUpgrade(book, results[:1]); m != nil {
		t.Errorf("matched a different author's Dust: %+v", m)
	}
	// No author on the library side: the single same-title result is taken.
	if m := matchUpgrade(Book{Title: "Dust"}, results[1:2]); m == nil || m.Key != "hc:2" {
		t.Errorf("authorless book didn't take the sole same-title result: %+v", m)
	}
	if m := matchUpgrade(Book{Title: "Dust"}, results[:2]); m == nil || m.Key != "hc:1" {
		t.Errorf("authorless book with two same-title results should take the first: %+v", m)
	}
}

func TestAuthorsOverlap(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"Hugh Howey", "Howey, Hugh", true},
		{"H. Howey", "Hugh Howey", true},
		{"J.K. Rowling", "Rowling, J. K.", true},
		{"Hugh Howey", "Elizabeth Bear", false},
		{"Jo Nesbø", "Jo Smith", false}, // "jo" is too short to count
	} {
		if got := authorsOverlap(c.a, c.b); got != c.want {
			t.Errorf("authorsOverlap(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// The re-match must read titles the way catalogues write them, and take an
// authorless catalogue entry when that is all the catalogue has under the title.
func TestMatchUpgradeReadsSubtitlesAndAuthorlessEntries(t *testing.T) {
	book := Book{Title: "The Final Empire", Author: "Brandon Sanderson"}
	results := []metadata.BookResult{
		{Key: "hc:k", Title: "The Final Empire", Author: "Wm. H. Kötke"},
		{Key: "hc:m", Title: "Mistborn: The Final Empire", Author: "Brandon Sanderson"},
	}
	if m := matchUpgrade(book, results); m == nil || m.Key != "hc:m" {
		t.Errorf("matched %+v, want the Mistborn entry (subtitle is the title)", m)
	}
	sky := Book{Title: "Skyward Flight", Author: "Brandon Sanderson"}
	if m := matchUpgrade(sky, []metadata.BookResult{{Key: "hc:s", Title: "Skyward Flight"}}); m == nil || m.Key != "hc:s" {
		t.Errorf("an authorless same-title entry should be taken: %+v", m)
	}
	if m := matchUpgrade(sky, []metadata.BookResult{{Key: "hc:x", Title: "Skyward Flight", Author: "Someone Else"}}); m != nil {
		t.Errorf("a different author's book of the same title must not match: %+v", m)
	}
	amp := Book{Title: "The Songbird and the Heart of Stone", Author: "Carissa Broadbent"}
	if m := matchUpgrade(amp, []metadata.BookResult{{Key: "hc:a", Title: "The Songbird & the Heart of Stone", Author: "Carissa Broadbent"}}); m == nil {
		t.Error("& and 'and' are the same title")
	}
}

// A record that is a guide to a book names the book and its author in its title.
func TestDerivedWork(t *testing.T) {
	for _, c := range []struct {
		title, author, work, by string
		ok                      bool
	}{
		{"LinguiSystems novel guide for Harry Potter and the goblet of fire by J.K. Rowling", "Laura Sauser", "Harry Potter and the goblet of fire", "J.K. Rowling", true},
		{"Harry Potter and the prisoner of Azkaban by J.K. Rowling", "Linda Ward Beech", "Harry Potter and the prisoner of Azkaban", "J.K. Rowling", true},
		{"Dune by Frank Herbert", "Frank Herbert", "", "", false},
		{"Death by Chocolate", "Sally Berneathy", "", "", false},
		{"The Final Empire", "Brandon Sanderson", "", "", false},
	} {
		work, by, ok := derivedWork(c.title, c.author)
		if ok != c.ok || work != c.work || by != c.by {
			t.Errorf("derivedWork(%q, %q) = (%q, %q, %v), want (%q, %q, %v)", c.title, c.author, work, by, ok, c.work, c.by, c.ok)
		}
	}
}
