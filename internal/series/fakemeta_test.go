package series

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/store"
)

// fakeMeta is a metadata.SeriesProvider whose answer the test sets — and changes between
// refreshes — to model a source failing, a key being added, or TMDB adding an episode.
type fakeMeta struct{ d metadata.SeriesDetails }

func (f *fakeMeta) Available() bool { return true }
func (f *fakeMeta) SearchSeries(context.Context, string) ([]metadata.SeriesResult, error) {
	return nil, nil
}
func (f *fakeMeta) GetSeries(context.Context, int) (*metadata.SeriesDetails, error) {
	cp := f.d
	return &cp, nil
}

// listing builds a season listing from episode counts, e.g. listing(3, 2) is S1 with three
// episodes and S2 with two. abs, when given, supplies real (TVDB-style) absolute numbers in
// order; otherwise none are given and the series module counts them.
func listing(counts ...int) []metadata.SeasonDetails {
	var out []metadata.SeasonDetails
	for i, n := range counts {
		sd := metadata.SeasonDetails{SeasonNumber: i + 1}
		for e := 1; e <= n; e++ {
			sd.Episodes = append(sd.Episodes, metadata.EpisodeDetails{EpisodeNumber: e, Title: "orig", AirDate: "2020-01-01"})
		}
		out = append(out, sd)
	}
	return out
}

// withAbsolutes stamps real absolute numbers 1..N across the non-special seasons, the way
// TVDB supplies them.
func withAbsolutes(seasons []metadata.SeasonDetails) []metadata.SeasonDetails {
	abs := 0
	for i := range seasons {
		for j := range seasons[i].Episodes {
			abs++
			seasons[i].Episodes[j].AbsoluteNumber = abs
		}
	}
	return seasons
}

func refreshTestService(t *testing.T, d metadata.SeriesDetails) (*Service, *fakeMeta, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	fm := &fakeMeta{d: d}
	return NewService(st.DB(), fm, t.TempDir(), slog.New(slog.NewTextHandler(io.Discard, nil))), fm, context.Background()
}

func animeDetails() metadata.SeriesDetails {
	return metadata.SeriesDetails{
		SeriesResult: metadata.SeriesResult{TMDBID: 9, Title: "Anime Show"},
		OriginalLang: "ja", Genres: []string{"Animation"}, TVDBID: 99,
	}
}

func standardDetails() metadata.SeriesDetails {
	return metadata.SeriesDetails{SeriesResult: metadata.SeriesResult{TMDBID: 8, Title: "Standard Show"}, TVDBID: 88}
}

func mustFile(t *testing.T, svc *Service, ctx context.Context, id int64, season, episode int) string {
	t.Helper()
	p, err := svc.EpisodeFilePath(ctx, id, season, episode)
	if err != nil {
		t.Fatal(err)
	}
	return p
}
