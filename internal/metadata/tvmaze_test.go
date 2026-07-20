package metadata

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Parks and Recreation season 6, as TVmaze lists it — the release convention, with the
// two-part "London" kept as two episodes. TMDB merges them into one entry, which is what
// put every later episode of that season a slot out.
const tvmazeS6 = `[
 {"season":6,"number":1,"name":"London (1)","airdate":"2013-09-26","runtime":22,"summary":"<p>Leslie goes to <b>London</b>.</p>","image":{"medium":"m1","original":"o1"}},
 {"season":6,"number":2,"name":"London (2)","airdate":"2013-09-26","runtime":22,"summary":"<p>Part two.</p>"},
 {"season":6,"number":3,"name":"The Pawnee-Eagleton Tip-Off Classic","airdate":"2013-10-03","runtime":22},
 {"season":6,"number":4,"name":"Doppelgangers","airdate":"2013-10-10","runtime":22},
 {"season":6,"number":null,"name":"Unnumbered extra"},
 {"season":0,"number":1,"name":"A Special","airdate":"2013-01-01","runtime":5}
]`

func tvmazeStub(t *testing.T, episodes string) *TVmaze {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasPrefix(r.URL.Path, "/lookup/shows"):
			if r.URL.Query().Get("thetvdb") == "99999" { // deliberately absent from TVmaze
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, `{"id":42}`)
		case strings.HasPrefix(r.URL.Path, "/shows/42/episodes"):
			if r.URL.Query().Get("specials") != "1" {
				t.Error("specials must be requested, or season 0 disappears from the library")
			}
			_, _ = io.WriteString(w, episodes)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return &TVmaze{http: srv.Client(), base: srv.URL}
}

// The whole point: numbering that matches how releases are actually named.
func TestTVmazeEpisodeNumbering(t *testing.T) {
	seasons, err := tvmazeStub(t, tvmazeS6).Episodes(context.Background(), 1234, "tt0000001")
	if err != nil {
		t.Fatalf("Episodes: %v", err)
	}
	if len(seasons) != 2 {
		t.Fatalf("want the specials season and season 6, got %d: %+v", len(seasons), seasons)
	}
	// Specials must survive — they're part of the library today.
	if seasons[0].SeasonNumber != 0 {
		t.Errorf("first season = %d, want the specials season 0", seasons[0].SeasonNumber)
	}

	s6 := seasons[1]
	if s6.SeasonNumber != 6 {
		t.Fatalf("second season = %d, want 6", s6.SeasonNumber)
	}
	want := []struct {
		num   int
		title string
	}{
		{1, "London (1)"},
		{2, "London (2)"},
		{3, "The Pawnee-Eagleton Tip-Off Classic"},
		{4, "Doppelgangers"},
	}
	if len(s6.Episodes) != len(want) {
		t.Fatalf("season 6 has %d episodes, want %d — an unnumbered entry can't be placed and must be dropped", len(s6.Episodes), len(want))
	}
	for i, w := range want {
		if got := s6.Episodes[i]; got.EpisodeNumber != w.num || got.Title != w.title {
			t.Errorf("episode %d = %d/%q, want %d/%q", i, got.EpisodeNumber, got.Title, w.num, w.title)
		}
	}

	// HTML summaries must be flattened, or the markup shows up verbatim in the UI.
	if got := s6.Episodes[0].Overview; got != "Leslie goes to London." {
		t.Errorf("overview = %q, want the tags stripped", got)
	}
	if got := s6.Episodes[0].StillURL; got != "o1" {
		t.Errorf("still = %q, want the original image", got)
	}
	// Runtime drives the bitrate upgrade comparison, so it has to come through.
	if s6.Episodes[0].Runtime != 22 {
		t.Errorf("runtime = %d, want 22", s6.Episodes[0].Runtime)
	}
}

// A show TVmaze doesn't carry is an ordinary answer, not a failure — the caller keeps its
// existing numbering.
func TestTVmazeMissIsNotAnError(t *testing.T) {
	seasons, err := tvmazeStub(t, tvmazeS6).Episodes(context.Background(), 99999, "")
	if err != nil {
		t.Errorf("a show absent from TVmaze must not be an error, got %v", err)
	}
	if len(seasons) != 0 {
		t.Errorf("want no seasons, got %+v", seasons)
	}
}

func TestStripHTML(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<p>Hello <b>world</b>.</p>", "Hello world."},
		{"plain text", "plain text"},
		{"", ""},
		{"unclosed <tag", "unclosed"},
	}
	for _, c := range cases {
		if got := stripHTML(c.in); got != c.want {
			t.Errorf("stripHTML(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// The decorator must keep the primary's show data and swap only the episode listing.
func TestEpisodeSourceReplacesOnlyNumbering(t *testing.T) {
	primary := &stubSeries{d: &SeriesDetails{
		SeriesResult: SeriesResult{Title: "Parks and Recreation"},
		TVDBID:       1234,
		Seasons: []SeasonDetails{{
			SeasonNumber: 6, Name: "Season 6", Overview: "TMDB blurb", PosterURL: "tmdb-poster.jpg",
			Episodes: []EpisodeDetails{{EpisodeNumber: 1, Title: "London"}}, // TMDB's merged entry
		}},
	}}
	src := &stubEpisodes{seasons: []SeasonDetails{{
		SeasonNumber: 6,
		Episodes: []EpisodeDetails{
			{EpisodeNumber: 1, Title: "London (1)"},
			{EpisodeNumber: 2, Title: "London (2)"},
		},
	}}}

	got, err := NewSeriesWithEpisodes(primary, src, slog.Default()).GetSeries(context.Background(), 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Seasons) != 1 || len(got.Seasons[0].Episodes) != 2 {
		t.Fatalf("numbering was not replaced: %+v", got.Seasons)
	}
	// Season presentation stays the primary's — the second source owns numbering, not art.
	sn := got.Seasons[0]
	if sn.Name != "Season 6" || sn.Overview != "TMDB blurb" || sn.PosterURL != "tmdb-poster.jpg" {
		t.Errorf("season presentation was clobbered: %+v", sn)
	}
	if got.Title != "Parks and Recreation" {
		t.Errorf("show data was clobbered: %q", got.Title)
	}
}

// Every failure mode must leave the show working on the primary's numbering. A metadata
// source being down is not a reason a show can't be added or refreshed, and replacing a
// full listing with a broken one would mark most of a library as nonexistent.
func TestEpisodeSourceFallsBack(t *testing.T) {
	tmdbSeasons := []SeasonDetails{{SeasonNumber: 1, Episodes: []EpisodeDetails{{EpisodeNumber: 1, Title: "Pilot"}}}}
	base := func() *stubSeries {
		return &stubSeries{d: &SeriesDetails{SeriesResult: SeriesResult{Title: "Show"}, TVDBID: 1234, Seasons: tmdbSeasons}}
	}

	for name, src := range map[string]*stubEpisodes{
		"lookup failed": {err: io.ErrUnexpectedEOF},
		"not carried":   {seasons: nil},
		"empty listing": {seasons: []SeasonDetails{{SeasonNumber: 1}}},
	} {
		got, err := NewSeriesWithEpisodes(base(), src, slog.Default()).GetSeries(context.Background(), 1)
		if err != nil {
			t.Errorf("%s: returned an error instead of falling back: %v", name, err)
			continue
		}
		if len(got.Seasons) != 1 || len(got.Seasons[0].Episodes) != 1 || got.Seasons[0].Episodes[0].Title != "Pilot" {
			t.Errorf("%s: the primary's listing should have survived, got %+v", name, got.Seasons)
		}
	}

	// No external ids means no lookup is even possible.
	noIDs := &stubSeries{d: &SeriesDetails{SeriesResult: SeriesResult{Title: "Show"}, Seasons: tmdbSeasons}}
	src := &stubEpisodes{seasons: []SeasonDetails{{SeasonNumber: 9, Episodes: []EpisodeDetails{{EpisodeNumber: 1}}}}}
	got, _ := NewSeriesWithEpisodes(noIDs, src, slog.Default()).GetSeries(context.Background(), 1)
	if got.Seasons[0].SeasonNumber != 1 {
		t.Error("with no TVDB or IMDb id there is nothing to match on — the primary must be kept")
	}
}

// A nil or unavailable source must return the primary untouched, not a wrapper that
// fails on every call.
func TestNilEpisodeSourceReturnsPrimary(t *testing.T) {
	p := &stubSeries{d: &SeriesDetails{SeriesResult: SeriesResult{Title: "Show"}}}
	if got := NewSeriesWithEpisodes(p, nil, slog.Default()); got != SeriesProvider(p) {
		t.Error("a nil episode source should hand back the primary unchanged")
	}
}

type stubSeries struct{ d *SeriesDetails }

func (s *stubSeries) Available() bool                                              { return true }
func (s *stubSeries) SearchSeries(context.Context, string) ([]SeriesResult, error) { return nil, nil }
func (s *stubSeries) GetSeries(context.Context, int) (*SeriesDetails, error) {
	cp := *s.d
	return &cp, nil
}

type stubEpisodes struct {
	seasons []SeasonDetails
	err     error
}

func (s *stubEpisodes) Available() bool { return true }
func (s *stubEpisodes) Episodes(context.Context, int, string) ([]SeasonDetails, error) {
	return s.seasons, s.err
}
