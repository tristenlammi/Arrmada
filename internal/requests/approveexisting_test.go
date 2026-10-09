package requests

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

// fakeMovieMeta answers GetMovie for any id with a fixed title.
type fakeMovieMeta struct{ metadata.MovieProvider }

func (fakeMovieMeta) GetMovie(_ context.Context, id int) (*metadata.MovieDetails, error) {
	return &metadata.MovieDetails{MovieResult: metadata.MovieResult{TMDBID: id, Title: "Heat", Year: 1995}, Status: "Released"}, nil
}

// fakeSeriesMeta answers GetSeries with a listing the test sets.
type fakeSeriesMeta struct{ d metadata.SeriesDetails }

func (f *fakeSeriesMeta) Available() bool { return true }
func (f *fakeSeriesMeta) SearchSeries(context.Context, string) ([]metadata.SeriesResult, error) {
	return nil, nil
}
func (f *fakeSeriesMeta) GetSeries(_ context.Context, id int) (*metadata.SeriesDetails, error) {
	cp := f.d
	cp.TMDBID = id
	return &cp, nil
}

// showListing is a show whose seasons have the given episode counts, every episode aired.
func showListing(counts ...int) metadata.SeriesDetails {
	d := metadata.SeriesDetails{SeriesResult: metadata.SeriesResult{Title: "Show"}}
	for i, n := range counts {
		sd := metadata.SeasonDetails{SeasonNumber: i + 1}
		for e := 1; e <= n; e++ {
			sd.Episodes = append(sd.Episodes, metadata.EpisodeDetails{EpisodeNumber: e, AirDate: "2020-01-01"})
		}
		d.Seasons = append(d.Seasons, sd)
	}
	return d
}

type approveFixture struct {
	s    *Service
	jobs *recordJobs
	meta *fakeSeriesMeta
	ctx  context.Context
}

// newApproveFixture is a requests service over a fresh store with real movies, series
// and books services on fake catalogues, and a job recorder in place of the runner.
func newApproveFixture(t *testing.T, show metadata.SeriesDetails) approveFixture {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	meta := &fakeSeriesMeta{d: show}
	rec := &recordJobs{}
	s := &Service{
		repo:    NewRepo(db),
		movies:  movies.NewService(db, fakeMovieMeta{}, nil, t.TempDir(), "", nil, log),
		series:  series.NewService(db, meta, t.TempDir(), log),
		books:   books.NewService(db, nil, log),
		quality: quality.NewService(db),
		log:     log,
	}
	s.SetJobs(rec)
	return approveFixture{s: s, jobs: rec, meta: meta, ctx: context.Background()}
}

// searched lists the targets of the search jobs submitted so far, by kind.
func (f approveFixture) searched(kind string) []string {
	var out []string
	for _, sp := range f.jobs.specs {
		if sp.Kind == kind {
			out = append(out, sp.Target)
		}
	}
	return out
}

// Approving a request for a show a library scan added (unmonitored, nothing wanted)
// monitors it, starts a search, and says so in the show's History.
func TestApproveExistingUnmonitoredSeriesMonitorsAndSearches(t *testing.T) {
	f := newApproveFixture(t, showListing(2, 2))
	sr, err := f.s.series.Add(f.ctx, 77, "", false) // as a library scan adds it
	if err != nil {
		t.Fatal(err)
	}
	req, err := f.s.repo.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Status: StatusPending, RequestedBy: 7, RequestedByName: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := f.s.Approve(f.ctx, req.ID, "")
	if err != nil {
		t.Fatal(err)
	}
	if got.Status != StatusApproved {
		t.Errorf("status = %q", got.Status)
	}
	after, _ := f.s.series.Get(f.ctx, sr.ID)
	if !after.Monitored || after.Stats == nil || after.Stats.Missing != 4 {
		t.Errorf("after approve: gate %v, stats %+v; want monitored with 4 wanted episodes", after.Monitored, after.Stats)
	}
	if s := f.searched("series.search"); len(s) != 1 || s[0] != fmt.Sprintf("series:%d", sr.ID) {
		t.Errorf("series searches = %v, want one for the show", s)
	}
	evs, _ := f.s.series.Events(f.ctx, sr.ID, 5)
	if len(evs) == 0 || !strings.Contains(evs[0].Detail, "Monitored by request from alice") {
		t.Errorf("history = %+v", evs)
	}
}

// A show whose every aired episode is already on disk is just approved: no monitoring
// change, no search.
func TestApproveExistingCompleteSeriesJustApproves(t *testing.T) {
	f := newApproveFixture(t, showListing(1))
	sr, err := f.s.series.Add(f.ctx, 77, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.s.series.MarkEpisodeImported(f.ctx, sr.ID, 1, 1, "/tv/show/s01e01.mkv", 1); err != nil {
		t.Fatal(err)
	}
	req, _ := f.s.repo.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Status: StatusPending, RequestedBy: 7})
	if _, err := f.s.Approve(f.ctx, req.ID, ""); err != nil {
		t.Fatal(err)
	}
	after, _ := f.s.series.Get(f.ctx, sr.ID)
	if after.Monitored || len(f.searched("series.search")) != 0 {
		t.Errorf("gate %v, searches %v; want nothing changed", after.Monitored, f.jobs.specs)
	}
}

// Approving a request for a movie the library has without a file monitors and searches it.
func TestApproveExistingMovieWithoutFileSearches(t *testing.T) {
	f := newApproveFixture(t, metadata.SeriesDetails{})
	m, err := movies.NewRepo(f.s.repo.db).Create(f.ctx, movies.Movie{TMDBID: 949, Title: "Heat", Year: 1995})
	if err != nil {
		t.Fatal(err)
	}
	if m.Monitored {
		t.Fatal("fixture movie should start unmonitored")
	}
	req, _ := f.s.repo.Create(f.ctx, Request{MediaType: "movie", TMDBID: 949, Title: "Heat", Status: StatusPending, RequestedBy: 7, RequestedByName: "bob"})
	if _, err := f.s.Approve(f.ctx, req.ID, ""); err != nil {
		t.Fatal(err)
	}
	after, err := f.s.movies.Get(f.ctx, m.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !after.Monitored {
		t.Error("the movie is still unmonitored")
	}
	if s := f.searched("movie.search"); len(s) != 1 || s[0] != fmt.Sprintf("movie:%d", m.ID) {
		t.Errorf("movie searches = %v", s)
	}
}
