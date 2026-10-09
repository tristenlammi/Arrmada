package series

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// presetShow seeds three seasons plus a monitored special: S01E01 aired with a file,
// S01E02 aired without one, S02E01 aired, S02E02 future, S03E01 future, S03E02 no date.
func presetShow(t *testing.T) (*Repo, int64) {
	t.Helper()
	repo, ctx := testRepo(t)
	sr, err := repo.Create(ctx, Series{TMDBID: 1, Title: "Show", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	ep := func(s, e int, air string) Episode { return Episode{SeasonNumber: s, EpisodeNumber: e, AirDate: air} }
	if err := repo.InsertSeasons(ctx, sr.ID, []Season{
		{SeasonNumber: 0, Episodes: []Episode{ep(0, 1, "2020-01-01")}},
		{SeasonNumber: 1, Episodes: []Episode{ep(1, 1, "2020-01-01"), ep(1, 2, "2020-01-08")}},
		{SeasonNumber: 2, Episodes: []Episode{ep(2, 1, "2021-01-01"), ep(2, 2, "2999-01-01")}},
		{SeasonNumber: 3, Episodes: []Episode{ep(3, 1, "2999-02-01"), ep(3, 2, "")}},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`UPDATE episodes SET monitored = 1 WHERE series_id = ? AND season_number = 0`, sr.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetEpisodeFile(ctx, sr.ID, 1, 1, "/tv/S01E01.mkv", 1); err != nil {
		t.Fatal(err)
	}
	return repo, sr.ID
}

func monitoredSet(t *testing.T, repo *Repo, id int64) (eps string, seasons string) {
	t.Helper()
	ss, err := repo.SeasonsFor(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	var e, s []string
	for _, sn := range ss {
		if sn.Monitored {
			s = append(s, fmt.Sprint(sn.SeasonNumber))
		}
		for _, ep := range sn.Episodes {
			if ep.Monitored {
				e = append(e, fmtSxxExx(ep.SeasonNumber, ep.EpisodeNumber))
			}
		}
	}
	sort.Strings(e)
	return strings.Join(e, " "), strings.Join(s, " ")
}

func TestApplyMonitorPreset(t *testing.T) {
	for _, tc := range []struct {
		preset, eps, seasons string
		newSeasons           bool
	}{
		{PresetAll, "S00E01 S01E01 S01E02 S02E01 S02E02 S03E01 S03E02", "0 1 2 3", true},
		{PresetFuture, "S00E01 S02E02 S03E01 S03E02", "0 2 3", true},
		{PresetMissing, "S00E01 S01E02 S02E01 S02E02 S03E01 S03E02", "0 1 2 3", true},
		{PresetExisting, "S00E01 S01E01 S02E02 S03E01 S03E02", "0 1 2 3", true},
		{PresetFirstSeason, "S00E01 S01E01 S01E02", "0 1", false},
		{PresetLatestSeason, "S00E01 S03E01 S03E02", "0 3", true},
		{PresetNone, "", "", false},
	} {
		t.Run(tc.preset, func(t *testing.T) {
			repo, id := presetShow(t)
			if err := repo.ApplyMonitorPreset(t.Context(), id, tc.preset, nil); err != nil {
				t.Fatal(err)
			}
			eps, seasons := monitoredSet(t, repo, id)
			if eps != tc.eps {
				t.Errorf("monitored episodes = %q, want %q", eps, tc.eps)
			}
			if seasons != tc.seasons {
				t.Errorf("monitored seasons = %q, want %q (derived from their episodes)", seasons, tc.seasons)
			}
			if sr, _ := repo.Get(t.Context(), id); sr.MonitorNewSeasons != tc.newSeasons {
				t.Errorf("monitor_new_seasons = %v, want %v", sr.MonitorNewSeasons, tc.newSeasons)
			}
		})
	}
	repo, id := presetShow(t)
	off := false
	if err := repo.ApplyMonitorPreset(t.Context(), id, PresetLatestSeason, &off); err != nil {
		t.Fatal(err)
	}
	if sr, _ := repo.Get(t.Context(), id); sr.MonitorNewSeasons {
		t.Error("an explicit monitor_new_seasons overrides the preset's own choice")
	}
	if err := repo.ApplyMonitorPreset(t.Context(), id, "everything", nil); !errors.Is(err, ErrUnknownPreset) {
		t.Errorf("unknown preset = %v, want ErrUnknownPreset", err)
	}
	if err := repo.ApplyMonitorPreset(t.Context(), 999, PresetAll, nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing show = %v, want ErrNotFound", err)
	}
}

// Monitoring one episode in an unmonitored season makes the season monitored, so the
// sweep (which needs both flags) actually searches it.
func TestEpisodeMonitorSyncsSeasonFlag(t *testing.T) {
	repo, id := presetShow(t)
	ctx := t.Context()
	if err := repo.ApplyMonitorPreset(ctx, id, PresetNone, nil); err != nil {
		t.Fatal(err)
	}
	ss, _ := repo.SeasonsFor(ctx, id)
	var s2e1 int64
	for _, sn := range ss {
		for _, e := range sn.Episodes {
			if e.SeasonNumber == 2 && e.EpisodeNumber == 1 {
				s2e1 = e.ID
			}
		}
	}
	if err := repo.SetEpisodeMonitored(ctx, s2e1, true); err != nil {
		t.Fatal(err)
	}
	if _, seasons := monitoredSet(t, repo, id); seasons != "2" {
		t.Errorf("monitored seasons = %q, want just 2", seasons)
	}
	if err := repo.SetEpisodeMonitored(ctx, s2e1, false); err != nil {
		t.Fatal(err)
	}
	if _, seasons := monitoredSet(t, repo, id); seasons != "" {
		t.Errorf("monitored seasons = %q, want none once its last episode is off", seasons)
	}
	if err := repo.SetEpisodeMonitored(ctx, 99999, true); !errors.Is(err, ErrNotFound) {
		t.Errorf("missing episode = %v, want ErrNotFound", err)
	}
}

func presetDetails() metadata.SeriesDetails {
	d := standardDetails()
	d.Seasons = []metadata.SeasonDetails{
		{SeasonNumber: 0, Episodes: []metadata.EpisodeDetails{{EpisodeNumber: 1, AirDate: "2020-01-01"}}},
		{SeasonNumber: 1, Episodes: []metadata.EpisodeDetails{{EpisodeNumber: 1, AirDate: "2020-01-01"}, {EpisodeNumber: 2, AirDate: "2020-01-08"}}},
		{SeasonNumber: 2, Episodes: []metadata.EpisodeDetails{{EpisodeNumber: 1, AirDate: "2999-01-01"}}},
	}
	return d
}

// Adding with "Future episodes" leaves every aired episode unmonitored, so the first sweep
// grabs nothing old; with no preset given, the configured default applies.
func TestAddAppliesPreset(t *testing.T) {
	d := presetDetails()
	svc, _, ctx := refreshTestService(t, d)
	sr, err := svc.AddWith(ctx, d.TMDBID, "", AddOptions{Monitored: true, Preset: PresetFuture})
	if err != nil {
		t.Fatal(err)
	}
	if eps, _ := monitoredSet(t, svc.repo, sr.ID); eps != "S02E01" {
		t.Errorf("future: monitored = %q, want only the unaired S02E01", eps)
	}
	if !sr.Monitored || !sr.MonitorNewSeasons {
		t.Errorf("returned show = monitored %v, new seasons %v", sr.Monitored, sr.MonitorNewSeasons)
	}

	d2 := presetDetails()
	svc2, _, ctx2 := refreshTestService(t, d2)
	svc2.SetMonitorDefaultFunc(func(_ context.Context) string { return PresetFirstSeason })
	sr2, err := svc2.AddWith(ctx2, d2.TMDBID, "", AddOptions{Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	if eps, _ := monitoredSet(t, svc2.repo, sr2.ID); eps != "S01E01 S01E02" {
		t.Errorf("default first_season: monitored = %q", eps)
	}
	if sr2.MonitorNewSeasons {
		t.Error("first season turns monitor new seasons off")
	}

	d3 := presetDetails()
	svc3, _, ctx3 := refreshTestService(t, d3)
	if _, err := svc3.AddWith(ctx3, d3.TMDBID, "", AddOptions{Preset: "bogus"}); !errors.Is(err, ErrUnknownPreset) {
		t.Errorf("unknown preset = %v, want ErrUnknownPreset and nothing added", err)
	}
	if all, _ := svc3.List(ctx3); len(all) != 0 {
		t.Errorf("an unknown preset added a show: %+v", all)
	}
}

// The library scan adds what it finds with nothing monitored and new seasons off.
func TestScanAddIsNone(t *testing.T) {
	d := presetDetails()
	svc, _, ctx := refreshTestService(t, d)
	sr, err := svc.Add(ctx, d.TMDBID, "n/a", false)
	if err != nil {
		t.Fatal(err)
	}
	if eps, seasons := monitoredSet(t, svc.repo, sr.ID); eps != "" || seasons != "" {
		t.Errorf("scanned show: monitored episodes %q, seasons %q, want none", eps, seasons)
	}
	if sr.Monitored || sr.MonitorNewSeasons {
		t.Errorf("scanned show = monitored %v, new seasons %v, want both off", sr.Monitored, sr.MonitorNewSeasons)
	}
}

// A preset given with the switch applies first, and the switch then changes alone: a
// preset that monitors nothing old isn't overridden by the nothing-monitored cascade.
func TestSetMonitoringPresetThenGate(t *testing.T) {
	d := presetDetails()
	d.Seasons = d.Seasons[:2] // only aired episodes
	svc, _, ctx := refreshTestService(t, d)
	sr, err := svc.Add(ctx, d.TMDBID, "", false)
	if err != nil {
		t.Fatal(err)
	}
	on := true
	if err := svc.SetMonitoring(ctx, sr.ID, MonitorChange{Monitored: &on, Preset: PresetFuture}); err != nil {
		t.Fatal(err)
	}
	if eps, _ := monitoredSet(t, svc.repo, sr.ID); eps != "" {
		t.Errorf("future on an all-aired show monitors nothing, got %q", eps)
	}
	if got, _ := svc.Get(ctx, sr.ID); !got.Monitored {
		t.Error("the gate should be on")
	}
	if err := svc.SetMonitoring(ctx, sr.ID, MonitorChange{Preset: "x"}); !errors.Is(err, ErrUnknownPreset) {
		t.Errorf("unknown preset = %v", err)
	}
}
