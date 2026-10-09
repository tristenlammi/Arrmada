package metadata

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

const tmdbSeriesDetailSample = `{
  "id": 1399, "name": "Show", "first_air_date": "2011-04-17", "status": "Returning Series",
  "seasons": [
    {"season_number": 2, "name": "Season 2", "episode_count": 10, "air_date": "2012-04-01", "poster_path": "/s2.jpg"},
    {"season_number": 0, "name": "Specials", "episode_count": 14, "air_date": "2010-12-05"},
    {"season_number": 1, "name": "Season 1", "episode_count": 10, "air_date": "2011-04-17"},
    {"season_number": 3, "name": "Season 3", "episode_count": 0, "air_date": null}
  ]
}`

// The detail record lists a series' regular seasons from the /tv payload it already
// fetches: episode counts parsed, specials dropped, in season order.
func TestSeasonSummariesParsed(t *testing.T) {
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.Write([]byte(tmdbSeriesDetailSample))
	}))
	defer srv.Close()

	tm := NewTMDB("k")
	tm.base = srv.URL
	d, err := tm.MediaDetails(context.Background(), "series", 1399)
	if err != nil {
		t.Fatalf("MediaDetails: %v", err)
	}
	if calls != 1 {
		t.Errorf("TMDB calls = %d, want 1 (no per-season fetches)", calls)
	}
	want := []SeasonSummary{
		{Number: 1, Name: "Season 1", EpisodeCount: 10, AirDate: "2011-04-17"},
		{Number: 2, Name: "Season 2", EpisodeCount: 10, AirDate: "2012-04-01", PosterURL: "https://image.tmdb.org/t/p/w500/s2.jpg"},
		{Number: 3, Name: "Season 3"},
	}
	if len(d.Seasons) != len(want) {
		t.Fatalf("seasons = %+v, want %+v", d.Seasons, want)
	}
	for i := range want {
		if d.Seasons[i] != want[i] {
			t.Errorf("season[%d] = %+v, want %+v", i, d.Seasons[i], want[i])
		}
	}

	// A movie has none.
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(tmdbMovieSample))
	}))
	defer srv2.Close()
	tm.base = srv2.URL
	m, err := tm.MediaDetails(context.Background(), "movie", 603)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Seasons) != 0 {
		t.Errorf("movie seasons = %+v, want none", m.Seasons)
	}
}
