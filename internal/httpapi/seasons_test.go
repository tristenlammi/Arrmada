package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/series"
)

// TestSeasonStates is the state rule across every state, for shows in and out of the
// library.
func TestSeasonStates(t *testing.T) {
	const today = "2026-10-10"
	summaries := []metadata.SeasonSummary{
		{Number: 1, Name: "Season 1", EpisodeCount: 10, AirDate: "2020-01-01"},
		{Number: 2, Name: "Season 2", EpisodeCount: 10, AirDate: "2021-01-01"},
		{Number: 3, Name: "Season 3", EpisodeCount: 10, AirDate: "2022-01-01"},
		{Number: 4, Name: "Season 4", EpisodeCount: 8, AirDate: "2023-01-01"},
		{Number: 5, Name: "Season 5", EpisodeCount: 8, AirDate: "2027-03-01"},
	}
	cases := []struct {
		name string
		lib  libraryShow
		want map[int]string
	}{
		{"not in the library", libraryShow{}, map[int]string{
			1: seasonRequestable, 2: seasonRequestable, 3: seasonRequestable, 4: seasonRequestable, 5: seasonUnaired,
		}},
		{"S1-3 on disk, S4 missing and unmonitored", libraryShow{in: true, monitored: true, progress: map[int]series.SeasonProgress{
			1: {Episodes: 10, Have: 10, Aired: 10},
			2: {Episodes: 10, Have: 10, Aired: 10},
			3: {Episodes: 10, Have: 10, Aired: 10},
			4: {Episodes: 8, Aired: 8},
			5: {Episodes: 8},
		}}, map[int]string{
			1: seasonInLibrary, 2: seasonInLibrary, 3: seasonInLibrary, 4: seasonRequestable, 5: seasonUnaired,
		}},
		{"partial, on the way, and a monitored season not out yet", libraryShow{in: true, monitored: true, progress: map[int]series.SeasonProgress{
			1: {Episodes: 10, Have: 4, Aired: 10},
			2: {Episodes: 10, Have: 4, Aired: 10, Monitored: true, Wanted: 6},
			3: {Episodes: 10, Aired: 10, Monitored: true, Wanted: 10},
			4: {Episodes: 8, Have: 8, Aired: 8, Monitored: true, Upcoming: 0},
			5: {Episodes: 8, Monitored: true, Upcoming: 8},
		}}, map[int]string{
			1: seasonPartial, 2: seasonOnTheWay, 3: seasonOnTheWay, 4: seasonInLibrary, 5: seasonOnTheWay,
		}},
		{"a paused show fetches nothing", libraryShow{in: true, monitored: false, progress: map[int]series.SeasonProgress{
			2: {Episodes: 10, Have: 4, Aired: 10, Monitored: true, Wanted: 6},
			3: {Episodes: 10, Aired: 10, Monitored: true, Wanted: 10},
		}}, map[int]string{
			1: seasonRequestable, 2: seasonPartial, 3: seasonRequestable, 4: seasonRequestable, 5: seasonUnaired,
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildSeasonStates(summaries, tc.lib, today)
			if len(got) != len(tc.want) {
				t.Fatalf("rows = %+v", got)
			}
			for _, row := range got {
				if row.State != tc.want[row.Number] {
					t.Errorf("S%d state = %q, want %q", row.Number, row.State, tc.want[row.Number])
				}
				wantReq := row.State == seasonRequestable || row.State == seasonPartial
				if row.Requestable != wantReq {
					t.Errorf("S%d requestable = %v", row.Number, row.Requestable)
				}
			}
		})
	}

	// A season only the library has is listed; TMDB's specials never are.
	got := buildSeasonStates([]metadata.SeasonSummary{{Number: 0}, {Number: 1, EpisodeCount: 2, AirDate: "2020-01-01"}},
		libraryShow{in: true, progress: map[int]series.SeasonProgress{2: {Episodes: 3, Have: 3, Aired: 3}}}, today)
	if len(got) != 2 || got[0].Number != 1 || got[1].Number != 2 || got[1].State != seasonInLibrary || got[1].Name != "Season 2" {
		t.Errorf("rows = %+v", got)
	}
}

// seasonsStub answers MediaDetails with a fixed season list.
type seasonsStub struct {
	stubDiscovery
	seasons []metadata.SeasonSummary
}

func (s seasonsStub) MediaDetails(context.Context, string, int) (*metadata.MediaDetail, error) {
	return &metadata.MediaDetail{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: s.seasons}, nil
}

// The endpoint answers a requester from the library and the cached TMDB record, with no
// monitoring flags and no names in it.
func TestSeriesSeasonsEndpoint(t *testing.T) {
	s := newRouteServer(t, func(d *Deps) {
		d.Series = series.NewService(d.Store.DB(), nil, t.TempDir(), d.Log)
		d.Discovery = seasonsStub{stubDiscovery{ok: true}, []metadata.SeasonSummary{
			{Number: 1, EpisodeCount: 2, AirDate: "2020-01-01"},
			{Number: 2, EpisodeCount: 1, AirDate: "2021-01-01"},
		}}
	})
	db := s.st.DB()
	for _, q := range []string{
		`INSERT INTO series (id, tmdb_id, title, monitored) VALUES (1, 77, 'Show', 1)`,
		`INSERT INTO seasons (series_id, season_number, monitored) VALUES (1, 1, 1), (1, 2, 0)`,
		`INSERT INTO episodes (series_id, season_number, episode_number, air_date, monitored, has_file) VALUES
			(1, 1, 1, '2020-01-01', 1, 1), (1, 1, 2, '2020-01-08', 1, 1), (1, 2, 1, '2021-01-01', 0, 0)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	_, cookie := s.user(t, "kid@example.com", auth.RoleRequester)
	rec := s.do("GET", "/api/v1/media/series/77/seasons", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	body := rec.Body.String()
	if strings.Contains(body, "monitored") || strings.Contains(body, "kid@") {
		t.Errorf("response leaks monitoring or names: %s", body)
	}
	var got struct {
		Seasons []seasonState `json:"seasons"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Seasons) != 2 || got.Seasons[0].State != seasonInLibrary || got.Seasons[1].State != seasonRequestable {
		t.Errorf("seasons = %+v, want S1 in_library, S2 requestable", got.Seasons)
	}
	if rec := s.do("GET", "/api/v1/media/series/0/seasons", cookie); rec.Code != http.StatusBadRequest {
		t.Errorf("bad id: HTTP %d", rec.Code)
	}
}
