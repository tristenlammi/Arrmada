package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/series"
)

// oneShowMeta answers every lookup with the same small show: S01 aired, S02 not yet.
type oneShowMeta struct{}

func (oneShowMeta) Available() bool { return true }
func (oneShowMeta) SearchSeries(context.Context, string) ([]metadata.SeriesResult, error) {
	return nil, nil
}
func (oneShowMeta) GetSeries(_ context.Context, id int) (*metadata.SeriesDetails, error) {
	return &metadata.SeriesDetails{
		SeriesResult: metadata.SeriesResult{TMDBID: id, Title: fmt.Sprintf("Show %d", id)},
		Seasons: []metadata.SeasonDetails{
			{SeasonNumber: 1, Episodes: []metadata.EpisodeDetails{{EpisodeNumber: 1, AirDate: "2020-01-01"}}},
			{SeasonNumber: 2, Episodes: []metadata.EpisodeDetails{{EpisodeNumber: 1, AirDate: "2999-01-01"}}},
		},
	}, nil
}

func seriesMonitorServer(t *testing.T) (*routeServer, *http.Cookie) {
	t.Helper()
	s := newRouteServer(t, func(d *Deps) {
		d.Series = series.NewService(d.Store.DB(), oneShowMeta{}, t.TempDir(), d.Log)
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	return s, mgr
}

// The add dialog's "Search on add" is for that add only: it used to rewrite the global
// setting. Off adds the show paused, with the preset's episode flags ready for later.
func TestAddSeriesSearchOnAddDoesNotWriteSetting(t *testing.T) {
	s, mgr := seriesMonitorServer(t)
	rec := s.doJSON("POST", "/api/v1/series", mgr, `{"tmdb_id": 5, "quality_profile": "hd", "search_on_add": false, "monitor": "future"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	if !s.deps.Settings.GetBool(context.Background(), keySearchOnAdd, true) {
		t.Error("adding with search_on_add=false changed the setting")
	}
	var got series.Series
	_ = json.Unmarshal(rec.Body.Bytes(), &got)
	if got.Monitored {
		t.Error("search on add off should add the show paused")
	}
	full, err := s.deps.Series.Get(context.Background(), got.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, sn := range full.Seasons {
		for _, e := range sn.Episodes {
			if want := sn.SeasonNumber == 2; e.Monitored != want {
				t.Errorf("S%02dE%02d monitored = %v, want %v (the future preset)", sn.SeasonNumber, e.EpisodeNumber, e.Monitored, want)
			}
		}
	}
}

func TestUnknownPresetIs400(t *testing.T) {
	s, mgr := seriesMonitorServer(t)
	if rec := s.doJSON("POST", "/api/v1/series", mgr, `{"tmdb_id": 5, "quality_profile": "hd", "monitor": "everything"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("add with an unknown preset: HTTP %d", rec.Code)
	}
	rec := s.doJSON("POST", "/api/v1/series", mgr, `{"tmdb_id": 6, "quality_profile": "hd", "search_on_add": false}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var sr series.Series
	_ = json.Unmarshal(rec.Body.Bytes(), &sr)
	path := fmt.Sprintf("/api/v1/series/%d/monitor", sr.ID)
	if rec := s.doJSON("PUT", path, mgr, `{"monitored": true, "preset": "bogus"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("monitor with an unknown preset: HTTP %d", rec.Code)
	}
	if got, _ := s.deps.Series.Get(context.Background(), sr.ID); got.Monitored {
		t.Error("a refused preset must change nothing, the gate included")
	}
	if rec := s.doJSON("PUT", "/api/v1/settings", mgr, `{"series_monitor_default": "bogus"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("setting an unknown default preset: HTTP %d", rec.Code)
	}

	// A known preset with the switch: latest season only, new seasons on.
	rec = s.doJSON("PUT", path, mgr, `{"monitored": true, "preset": "latest_season"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	got, _ := s.deps.Series.Get(context.Background(), sr.ID)
	if !got.Monitored || !got.MonitorNewSeasons {
		t.Errorf("after latest_season: monitored %v, new seasons %v", got.Monitored, got.MonitorNewSeasons)
	}
	// The checkbox alone doesn't pause the show.
	if rec := s.doJSON("PUT", path, mgr, `{"monitor_new_seasons": false}`); rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d", rec.Code)
	}
	if got, _ := s.deps.Series.Get(context.Background(), sr.ID); !got.Monitored || got.MonitorNewSeasons {
		t.Errorf("after the checkbox: monitored %v, new seasons %v", got.Monitored, got.MonitorNewSeasons)
	}
	// The default preset round-trips through Settings.
	if rec := s.doJSON("PUT", "/api/v1/settings", mgr, `{"series_monitor_default": "future"}`); rec.Code != http.StatusOK {
		t.Fatalf("save default preset: HTTP %d: %s", rec.Code, rec.Body)
	}
	rec = s.do("GET", "/api/v1/settings", mgr)
	var st map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &st)
	if st["series_monitor_default"] != "future" {
		t.Errorf("series_monitor_default = %v", st["series_monitor_default"])
	}
}
