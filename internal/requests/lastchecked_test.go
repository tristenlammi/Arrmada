package requests

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/metadata"
)

// ACQ-20: a requester's searching request says when it was last checked and how many
// searches in a row found nothing — from the sweep's own stamp, or a stored search if
// that is newer — and nothing else: no indexers, releases or reasons.
func TestTrackSaysWhenLastChecked(t *testing.T) {
	s, bookRepo, db, ctx := bookLinkFixture(t, catalogue{byKey: map[string]metadata.BookResult{}})
	s.coord = automation.New(nil, nil, nil, nil, db, nil, slog.New(slog.NewTextHandler(io.Discard, nil)), "")

	// A film the sweep has missed three times, last at 09:00.
	if _, err := db.Exec(`INSERT INTO movies (id, tmdb_id, title, year, monitored, has_file, last_search_at, search_misses)
		VALUES (1, 100, 'Dune', 2021, 1, 0, '2026-10-10 09:00:00', 3), (2, 101, 'Fresh', 2024, 1, 0, '', 0)`); err != nil {
		t.Fatal(err)
	}
	// A show never swept, but searched by hand at 11:30 (a stored attempt).
	if _, err := db.Exec(`INSERT INTO series (id, tmdb_id, title, year, monitored) VALUES (1, 200, 'Severance', 2022, 1)`); err != nil {
		t.Fatal(err)
	}
	manual := time.Date(2026, 10, 10, 11, 30, 0, 0, time.UTC)
	if _, err := db.Exec(`INSERT INTO search_attempts (media_type, media_id, scope, triggered_by, started_at, outcome, top_reason, example)
		VALUES ('series', 1, '', 'manual', ?, 'none_suitable', 'over_bitrate_ceiling', 'Severance.S01.2160p-SECRET')`, manual.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	// A book slowed to its monthly check, and one off the ladder (unmonitored).
	slowed, err := bookRepo.Create(ctx, books.Book{OLKey: "OL1W", Title: "Lost", Author: "A", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	off, err := bookRepo.Create(ctx, books.Book{OLKey: "OL2W", Title: "Off", Author: "B", Monitored: false})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE books SET last_search_at = '2026-10-01 09:00:00', search_misses = 12 WHERE id IN (?, ?)`, slowed.ID, off.ID); err != nil {
		t.Fatal(err)
	}
	for _, rq := range []Request{
		{MediaType: "movie", TMDBID: 100, Title: "Dune", Status: StatusApproved, RequestedBy: 7},
		{MediaType: "movie", TMDBID: 101, Title: "Fresh", Status: StatusApproved, RequestedBy: 7},
		{MediaType: "series", TMDBID: 200, Title: "Severance", Status: StatusApproved, RequestedBy: 7},
		{MediaType: "book", OLKey: "OL1W", BookID: slowed.ID, Title: "Lost", Status: StatusApproved, RequestedBy: 7},
		{MediaType: "book", OLKey: "OL2W", BookID: off.ID, Title: "Off", Status: StatusApproved, RequestedBy: 7},
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
	got := map[string]*Tracking{}
	for _, rq := range reqs {
		got[rq.Title] = rq.Tracking
	}

	if d := got["Dune"]; d == nil || d.Stage != StageSearching || d.Misses != 3 || d.LastSearchAt != "2026-10-10T09:00:00Z" || d.SearchStopped {
		t.Errorf("Dune = %+v, want searching, 3 misses, last checked 09:00", d)
	}
	if f := got["Fresh"]; f == nil || f.Stage != StageSearching || f.Misses != 0 || f.LastSearchAt != "" {
		t.Errorf("Fresh = %+v, want plain searching with no last check", f)
	}
	if sv := got["Severance"]; sv == nil || sv.LastSearchAt != manual.Format(time.RFC3339) {
		t.Errorf("Severance = %+v, want last checked at the manual search", sv)
	}
	if l := got["Lost"]; l == nil || !l.SearchStopped || l.Misses != 12 || l.LastSearchAt != "2026-10-01T09:00:00Z" {
		t.Errorf("Lost = %+v, want slowed to monthly", l)
	}
	if o := got["Off"]; o == nil || o.SearchStopped {
		t.Errorf("Off = %+v: an unmonitored book isn't 'searching again later'", o)
	}
}
