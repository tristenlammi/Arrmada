package metadata

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func numberingBase() *stubSeries {
	return &stubSeries{d: &SeriesDetails{
		SeriesResult:    SeriesResult{Title: "Show"},
		TVDBID:          1234,
		NumberingSource: "tmdb",
		Seasons:         []SeasonDetails{{SeasonNumber: 1, Episodes: []EpisodeDetails{{EpisodeNumber: 1}}}},
	}}
}

var goodListing = []SeasonDetails{{SeasonNumber: 1, Episodes: []EpisodeDetails{{EpisodeNumber: 1}, {EpisodeNumber: 2}}}}

// A numbering source that is up but errors means whatever listing comes back instead is a
// stand-in numbered by another convention. The series module must know, or a TVmaze
// timeout renumbers and moves a library. Skipping a source for an ordinary reason — no
// key, show not carried, a different season model — is the normal answer and isn't one.
func TestGetSeriesReportsFallbackWhenSourceErrors(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	cases := []struct {
		name     string
		sources  []EpisodeSource
		wantFrom string
		wantFell bool
	}{
		{"first source errors, second returns empty → primary, fallback",
			[]EpisodeSource{&stubEpisodes{name: "tvdb", err: io.ErrUnexpectedEOF}, &stubEpisodes{name: "tvmaze", seasons: nil}}, "tmdb", true},
		{"first source errors, second answers → second, still a fallback",
			[]EpisodeSource{&stubEpisodes{name: "tvdb", err: io.ErrUnexpectedEOF}, &stubEpisodes{name: "tvmaze", seasons: goodListing}}, "tvmaze", true},
		{"first source unavailable, second answers → second, not a fallback",
			[]EpisodeSource{&stubEpisodes{name: "tvdb", unavailable: true}, &stubEpisodes{name: "tvmaze", seasons: goodListing}}, "tvmaze", false},
		{"unavailable source, nothing else → primary, not a fallback",
			[]EpisodeSource{&stubEpisodes{name: "tvdb", unavailable: true}}, "tmdb", false},
		{"source doesn't carry the show → primary, not a fallback",
			[]EpisodeSource{&stubEpisodes{name: "tvmaze", seasons: nil}}, "tmdb", false},
		{"source models the show differently → primary, not a fallback",
			[]EpisodeSource{&stubEpisodes{name: "tvmaze", seasons: []SeasonDetails{{SeasonNumber: 2002, Episodes: []EpisodeDetails{{EpisodeNumber: 1}}}}}}, "tmdb", false},
	}
	for _, tc := range cases {
		got, err := NewSeriesWithEpisodes(numberingBase(), log, tc.sources...).GetSeries(ctx, 1)
		if err != nil {
			t.Errorf("%s: %v", tc.name, err)
			continue
		}
		if got.NumberingSource != tc.wantFrom || got.NumberingFallback != tc.wantFell {
			t.Errorf("%s: source=%q fallback=%v, want %q/%v", tc.name, got.NumberingSource, got.NumberingFallback, tc.wantFrom, tc.wantFell)
		}
	}

	// A primary that already knows its own listing is incomplete keeps saying so.
	partial := numberingBase()
	partial.d.NumberingFallback = true
	got, _ := NewSeriesWithEpisodes(partial, log, &stubEpisodes{name: "tvmaze", seasons: nil}).GetSeries(ctx, 1)
	if !got.NumberingFallback {
		t.Error("an incomplete primary listing must stay flagged as a fallback")
	}
	// ...unless a source's complete listing replaced it.
	got, _ = NewSeriesWithEpisodes(partial, log, &stubEpisodes{name: "tvmaze", seasons: goodListing}).GetSeries(ctx, 1)
	if got.NumberingFallback || got.NumberingSource != "tvmaze" {
		t.Errorf("a source's listing replaces the partial one: source=%q fallback=%v", got.NumberingSource, got.NumberingFallback)
	}
}

func TestGetSeriesNumberingSourceName(t *testing.T) {
	if (&TVDB{}).Name() != "tvdb" || NewTVmaze().Name() != "tvmaze" {
		t.Error("numbering sources must name themselves as stored in series.numbering_source")
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	got, _ := NewSeriesWithEpisodes(numberingBase(), log, &stubEpisodes{name: "tvdb", seasons: goodListing}).GetSeries(context.Background(), 1)
	if got.NumberingSource != "tvdb" {
		t.Errorf("NumberingSource = %q, want the source whose listing was used", got.NumberingSource)
	}
}
