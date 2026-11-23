package requests

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// importOpts is how the Overseerr import creates an approved request.
var importOpts = CreateOptions{AutoApprove: true, Silent: true, DeferSearch: true}

// Issue #130: an import of hundreds of approved requests must not become hundreds of
// searches. Each film it adds is monitored with the paced-search mark, so the missing
// sweeps take a few per run, and nothing is searched on the spot.
func TestImportedApprovedMoviesArePaced(t *testing.T) {
	f := newApproveFixture(t, metadata.SeriesDetails{})
	mv := f.s.movies.(*movies.Service)
	const n = 15
	for i := 0; i < n; i++ {
		got, _, err := f.s.Create(f.ctx, Request{MediaType: "movie", TMDBID: 1000 + i, Title: "x", RequestedBy: 7}, importOpts)
		if err != nil {
			t.Fatal(err)
		}
		if got.Status != StatusApproved {
			t.Fatalf("status = %q, want approved", got.Status)
		}
	}
	if len(f.jobs.specs) != 0 {
		t.Errorf("searches started = %d, want none (the sweeps take them)", len(f.jobs.specs))
	}
	marked, err := mv.PacedSearchIDs(f.ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(marked) != n {
		t.Errorf("paced films = %d, want %d", len(marked), n)
	}
	for id := range marked {
		m, err := mv.Get(f.ctx, id)
		if err != nil || !m.Monitored {
			t.Errorf("film %d: monitored %v, err %v; want monitored", id, m.Monitored, err)
		}
	}
}

// A film the library already had, unmonitored, starts being wanted through an imported
// request: it waits its turn too.
func TestImportedRequestForExistingUnmonitoredMovieIsPaced(t *testing.T) {
	f := newApproveFixture(t, metadata.SeriesDetails{})
	m, err := movies.NewRepo(f.s.repo.db).Create(f.ctx, movies.Movie{TMDBID: 949, Title: "Heat", Year: 1995, Status: "Released"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.s.Create(f.ctx, Request{MediaType: "movie", TMDBID: 949, Title: "Heat", RequestedBy: 7}, importOpts); err != nil {
		t.Fatal(err)
	}
	mv := f.s.movies.(*movies.Service)
	marked, _ := mv.PacedSearchIDs(f.ctx)
	after, _ := mv.Get(f.ctx, m.ID)
	if !marked[m.ID] || !after.Monitored {
		t.Errorf("paced %v, monitored %v; want both", marked[m.ID], after.Monitored)
	}
	if len(f.jobs.specs) != 0 {
		t.Errorf("searches = %+v, want none", f.jobs.specs)
	}
}

// An imported show is added paused, marked, then monitored — as a Sonarr import does.
func TestImportedApprovedSeriesIsPaced(t *testing.T) {
	f := newApproveFixture(t, showListing(2))
	got, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", RequestedBy: 7}, importOpts)
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusApproved {
		t.Fatalf("status = %q", got.Status)
	}
	sr, err := f.shows().GetByTMDB(f.ctx, 77)
	if err != nil {
		t.Fatal(err)
	}
	marked, _ := f.shows().PacedSearchIDs(f.ctx)
	if !marked[sr.ID] || !sr.Monitored {
		t.Errorf("paced %v, monitored %v; want both", marked[sr.ID], sr.Monitored)
	}
	if len(f.jobs.specs) != 0 {
		t.Errorf("searches = %+v, want none", f.jobs.specs)
	}
}

// An ordinary approval is unchanged: searched at once, no paced mark.
func TestOrdinaryApproveIsNotPaced(t *testing.T) {
	f := newApproveFixture(t, metadata.SeriesDetails{})
	if _, _, err := f.s.Create(f.ctx, Request{MediaType: "movie", TMDBID: 5, Title: "x", RequestedBy: 7}, CreateOptions{AutoApprove: true}); err != nil {
		t.Fatal(err)
	}
	marked, _ := f.s.movies.(*movies.Service).PacedSearchIDs(f.ctx)
	if len(marked) != 0 || len(f.searched("movie.search")) != 1 {
		t.Errorf("paced %v, searches %v; want no mark and one search", marked, f.jobs.specs)
	}
}

// OnShelf: a film with its file is on the shelf; one without, or not in the library, isn't.
func TestOnShelf(t *testing.T) {
	s, _, _ := quietFixture(t,
		movies.Movie{TMDBID: 603, Title: "Dune", HasFile: true},
		movies.Movie{TMDBID: 604, Title: "Wanted"},
	)
	ctx := context.Background()
	for _, c := range []struct {
		tmdb int
		want bool
	}{{603, true}, {604, false}, {605, false}} {
		got, err := s.OnShelf(ctx, "movie", c.tmdb)
		if err != nil || got != c.want {
			t.Errorf("OnShelf(%d) = %v, %v; want %v", c.tmdb, got, err, c.want)
		}
	}
}

// A bulk delete removes requests of every status, each on its own (an unknown id fails
// alone), and leaves the library as it is, as deleting one does.
func TestBulkDeleteAnyStatus(t *testing.T) {
	s, _, _ := quietFixture(t)
	ctx := context.Background()
	newReq := func(tmdb int, o CreateOptions) int64 {
		r, _, err := s.Create(ctx, Request{MediaType: "movie", TMDBID: tmdb, Title: "x", RequestedBy: 7}, o)
		if err != nil {
			t.Fatal(err)
		}
		return r.ID
	}
	pending := newReq(1, CreateOptions{})
	approved := newReq(2, importOpts)
	declined := newReq(3, CreateOptions{})
	if err := s.Decline(ctx, declined, DeclineOptions{}); err != nil {
		t.Fatal(err)
	}
	res := s.Bulk(ctx, BulkDelete, []int64{pending, approved, 999, declined}, "", "", 1, "admin")
	want := []bool{true, true, false, true}
	if len(res) != len(want) {
		t.Fatalf("results = %+v", res)
	}
	for i, r := range res {
		if r.OK != want[i] || (!r.OK && r.Error == "") {
			t.Errorf("result %d = %+v, want ok %v", i, r, want[i])
		}
	}
	for _, id := range []int64{pending, approved, declined} {
		if _, err := s.repo.Get(ctx, id); err == nil {
			t.Errorf("request %d is still there", id)
		}
	}
	// The film the approved request added stays in the library, monitored.
	m, err := s.movies.GetByTMDB(ctx, 2)
	if err != nil || !m.Monitored {
		t.Errorf("library film: %+v, %v; want it kept and monitored", m, err)
	}
}
