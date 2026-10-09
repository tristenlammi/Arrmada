package requests

import (
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/metadata"
)

// BOOK-08: an approved book request that has been searched for and not found says so,
// with when the next search is, instead of an open-ended "Searching". One just added
// (no misses yet) is still plainly searching.
func TestTrackBookNotFoundYet(t *testing.T) {
	s, repo, db, ctx := bookLinkFixture(t, catalogue{byKey: map[string]metadata.BookResult{}})
	lost, err := repo.Create(ctx, books.Book{OLKey: "OL1W", Title: "Dune", Author: "Frank Herbert", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := repo.Create(ctx, books.Book{OLKey: "OL2W", Title: "Emma", Author: "Jane Austen", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	last := time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC)
	if _, err := db.Exec(`UPDATE books SET last_search_at = ?, search_misses = 3 WHERE id = ?`,
		last.Format("2006-01-02 15:04:05"), lost.ID); err != nil {
		t.Fatal(err)
	}
	for _, rq := range []Request{
		{MediaType: "book", OLKey: "OL1W", BookID: lost.ID, Title: "Dune", Status: StatusApproved, RequestedBy: 7},
		{MediaType: "book", OLKey: "OL2W", BookID: fresh.ID, Title: "Emma", Status: StatusApproved, RequestedBy: 7},
	} {
		if _, err := s.repo.Create(ctx, rq); err != nil {
			t.Fatal(err)
		}
	}
	reqs, _, err := s.List(ctx, ListFilter{UserID: 7})
	if err != nil {
		t.Fatal(err)
	}
	s.Track(ctx, reqs, nil, true)
	byTitle := map[string]*Tracking{}
	for _, rq := range reqs {
		byTitle[rq.Title] = rq.Tracking
	}
	want := last.Add(books.SearchWait(3)).Format(time.RFC3339)
	if d := byTitle["Dune"]; d == nil || d.Stage != StageSearching || d.Note != "Not found yet" || d.NextCheckAt != want {
		t.Errorf("Dune tracking = %+v, want searching / Not found yet / next check %s", d, want)
	}
	if e := byTitle["Emma"]; e == nil || e.Stage != StageSearching || e.Note != "" || e.NextCheckAt != "" {
		t.Errorf("Emma tracking = %+v, want plain searching", e)
	}

	// Unmonitored, the sweep never looks again: still "not found", but no next check.
	if _, err := db.Exec(`UPDATE books SET monitored = 0 WHERE id = ?`, lost.ID); err != nil {
		t.Fatal(err)
	}
	reqs, _, err = s.List(ctx, ListFilter{UserID: 7})
	if err != nil {
		t.Fatal(err)
	}
	s.Track(ctx, reqs, nil, true)
	for _, rq := range reqs {
		if rq.Title == "Dune" && (rq.Tracking.Note != "Not found yet" || rq.Tracking.NextCheckAt != "") {
			t.Errorf("unmonitored Dune tracking = %+v, want Not found yet and no next check", rq.Tracking)
		}
	}
}
