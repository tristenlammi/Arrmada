package series

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

// GH-53: picking a preset on a show whose searching is off turns searching on, in the same
// write, and the show remembers the preset. Monitored false given with it keeps it off.
func TestPresetTurnsSearchingOn(t *testing.T) {
	d := presetDetails()
	svc, _, ctx := refreshTestService(t, d)
	sr, err := svc.Add(ctx, d.TMDBID, "n/a", false) // as the library scan adds a show
	if err != nil {
		t.Fatal(err)
	}
	if sr.Monitored || sr.MonitorPreset != PresetNone {
		t.Fatalf("scanned show = monitored %v, preset %q; want off and %q", sr.Monitored, sr.MonitorPreset, PresetNone)
	}
	if err := svc.SetMonitoring(ctx, sr.ID, MonitorChange{Preset: PresetMissing}); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Get(ctx, sr.ID)
	if !got.Monitored {
		t.Error("a preset should turn searching on")
	}
	if got.MonitorPreset != PresetMissing {
		t.Errorf("remembered preset = %q, want %q", got.MonitorPreset, PresetMissing)
	}
	if eps, _ := monitoredSet(t, svc.repo, sr.ID); eps != "S01E01 S01E02 S02E01" {
		t.Errorf("missing: monitored = %q", eps)
	}
	// Review (GH-53): turned on, it waits its turn like a show a Sonarr import added, so a
	// whole scanned library turned on at once isn't one burst of searches.
	if marked, err := svc.PacedSearchIDs(ctx); err != nil || !marked[sr.ID] {
		t.Errorf("a preset turning searching on should give the paced-search mark: %v (%v)", marked, err)
	}

	off := false
	if err := svc.SetMonitoring(ctx, sr.ID, MonitorChange{Preset: PresetAll, Monitored: &off}); err != nil {
		t.Fatal(err)
	}
	if got, _ := svc.Get(ctx, sr.ID); got.Monitored || got.MonitorPreset != PresetAll {
		t.Errorf("explicit off = monitored %v, preset %q; want off and all", got.Monitored, got.MonitorPreset)
	}
}

// A season or episode changed by hand after a preset makes the show Custom; a click that
// changes nothing doesn't. Turning searching on for a show with nothing monitored
// monitors every regular episode, which is the All preset.
func TestMonitorPresetBecomesCustom(t *testing.T) {
	repo, id := presetShow(t)
	ctx := t.Context()
	preset := func() string {
		t.Helper()
		s, err := repo.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		return s.MonitorPreset
	}
	if p := preset(); p != "" {
		t.Fatalf("a show with no preset applied = %q, want unknown", p)
	}
	if err := repo.ApplyMonitorPreset(ctx, id, PresetFuture, nil, nil); err != nil {
		t.Fatal(err)
	}
	if p := preset(); p != PresetFuture {
		t.Fatalf("after the preset = %q", p)
	}
	ss, _ := repo.SeasonsFor(ctx, id)
	var s1e1, s2e2 int64
	for _, sn := range ss {
		for _, e := range sn.Episodes {
			switch fmtSxxExx(e.SeasonNumber, e.EpisodeNumber) {
			case "S01E01":
				s1e1 = e.ID
			case "S02E02":
				s2e2 = e.ID
			}
		}
	}
	// S02E02 hasn't aired, so Future already monitors it: no change, still Future.
	if err := repo.SetEpisodeMonitored(ctx, s2e2, true); err != nil {
		t.Fatal(err)
	}
	if p := preset(); p != PresetFuture {
		t.Errorf("a no-op click = %q, want it to stay %q", p, PresetFuture)
	}
	if _, err := repo.SetEpisodesMonitored(ctx, id, []int64{s2e2}, true); err != nil {
		t.Fatal(err)
	}
	if p := preset(); p != PresetFuture {
		t.Errorf("a no-op bulk change = %q, want it to stay %q", p, PresetFuture)
	}
	if err := repo.SetEpisodeMonitored(ctx, s1e1, true); err != nil {
		t.Fatal(err)
	}
	if p := preset(); p != PresetCustom {
		t.Errorf("after an episode changed by hand = %q, want %q", p, PresetCustom)
	}

	if err := repo.ApplyMonitorPreset(ctx, id, PresetAll, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.SetEpisodesMonitored(ctx, id, []int64{s1e1}, false); err != nil {
		t.Fatal(err)
	}
	if p := preset(); p != PresetCustom {
		t.Errorf("after a bulk change = %q, want %q", p, PresetCustom)
	}

	if err := repo.ApplyMonitorPreset(ctx, id, PresetAll, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSeasonMonitored(ctx, id, 3, false); err != nil {
		t.Fatal(err)
	}
	if p := preset(); p != PresetCustom {
		t.Errorf("after a season changed by hand = %q, want %q", p, PresetCustom)
	}

	if err := repo.ApplyMonitorPreset(ctx, id, PresetNone, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetMonitored(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetMonitored(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if p := preset(); p != PresetAll {
		t.Errorf("searching on with nothing monitored = %q, want %q", p, PresetAll)
	}
}

// GH-52: the calendar says whether each episode is searched for from the episode's own
// flag, and tells a wanted episode of a show whose searching is off apart from one that
// isn't wanted (it used to send the show's flag alone).
func TestUpcomingEpisodesSearchState(t *testing.T) {
	d := presetDetails()
	svc, _, ctx := refreshTestService(t, d)
	sr, err := svc.AddWith(ctx, d.TMDBID, "", AddOptions{Monitored: true, Preset: PresetFirstSeason})
	if err != nil {
		t.Fatal(err)
	}
	state := func() map[string][2]bool {
		t.Helper()
		eps, err := svc.UpcomingEpisodes(ctx, "2000-01-01", "3000-01-01")
		if err != nil {
			t.Fatal(err)
		}
		out := map[string][2]bool{}
		for _, e := range eps {
			out[fmtSxxExx(e.Season, e.Episode)] = [2]bool{e.Monitored, e.SearchingOff}
		}
		return out
	}
	got := state()
	if got["S01E01"] != [2]bool{true, false} || got["S02E01"] != [2]bool{false, false} {
		t.Errorf("searching on: %v; want S01E01 searched, S02E01 not wanted", got)
	}
	if err := svc.SetMonitored(ctx, sr.ID, false); err != nil {
		t.Fatal(err)
	}
	got = state()
	if got["S01E01"] != [2]bool{false, true} || got["S02E01"] != [2]bool{false, false} {
		t.Errorf("searching off: %v; want S01E01 wanted with searching off, S02E01 not wanted", got)
	}
}

// Turning searching on marks a show to wait its turn; a show already searching, or one
// being turned off, isn't marked.
func TestSearchingOnMarksPaced(t *testing.T) {
	repo, id := presetShow(t) // searching on
	ctx := t.Context()
	marked := func() bool {
		t.Helper()
		m, err := repo.PacedSearchIDs(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return m[id]
	}
	on, off := true, false
	if err := repo.SetMonitored(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplyMonitorPreset(ctx, id, PresetAll, nil, &on); err != nil {
		t.Fatal(err)
	}
	if marked() {
		t.Fatal("a show whose searching was already on was marked")
	}
	if err := repo.SetMonitored(ctx, id, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplyMonitorPreset(ctx, id, PresetAll, nil, &off); err != nil {
		t.Fatal(err)
	}
	if marked() {
		t.Fatal("turning searching off marked the show")
	}
	if err := repo.SetMonitored(ctx, id, true); err != nil {
		t.Fatal(err)
	}
	if !marked() {
		t.Fatal("turning searching on should mark the show")
	}
}

// Review: seasons announced with no episodes yet change only their own flag; doing that by
// hand still makes the show Custom (it decides what a refresh will want).
func TestEmptySeasonByHandIsCustom(t *testing.T) {
	repo, id := presetShow(t)
	ctx := t.Context()
	if err := repo.InsertSeasons(ctx, id, []Season{{SeasonNumber: 4}}); err != nil {
		t.Fatal(err)
	}
	if err := repo.ApplyMonitorPreset(ctx, id, PresetFirstSeason, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSeasonMonitored(ctx, id, 4, true); err != nil {
		t.Fatal(err)
	}
	if s, _ := repo.Get(ctx, id); s.MonitorPreset != PresetCustom {
		t.Errorf("after monitoring an empty season by hand, preset = %q, want custom", s.MonitorPreset)
	}
	// Again: nothing changes, nothing more to say — but it stays Custom.
	if err := repo.ApplyMonitorPreset(ctx, id, PresetFirstSeason, nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetSeasonMonitored(ctx, id, 1, true); err != nil {
		t.Fatal(err)
	}
	if s, _ := repo.Get(ctx, id); s.MonitorPreset != PresetFirstSeason {
		t.Errorf("monitoring a season already monitored made the show %q", s.MonitorPreset)
	}
}

// Review: a request for seasons already monitored changes nothing, so the remembered preset
// stays; one that turns a season on makes it Custom.
func TestRequestChangingNothingKeepsPreset(t *testing.T) {
	repo, id := presetShow(t)
	ctx := t.Context()
	if err := repo.ApplyMonitorPreset(ctx, id, PresetAll, nil, nil); err != nil {
		t.Fatal(err)
	}
	before, _ := monitoredSet(t, repo, id)
	if _, err := repo.monitorForRequest(ctx, id, []int{2}, false, false); err != nil {
		t.Fatal(err)
	}
	after, _ := monitoredSet(t, repo, id)
	if before != after {
		t.Fatalf("a request for a monitored season changed flags: %q → %q", before, after)
	}
	if s, _ := repo.Get(ctx, id); s.MonitorPreset != PresetAll {
		t.Errorf("a request that changed nothing made the preset %q", s.MonitorPreset)
	}
	if err := repo.ApplyMonitorPreset(ctx, id, PresetFirstSeason, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.monitorForRequest(ctx, id, []int{2}, false, false); err != nil {
		t.Fatal(err)
	}
	if s, _ := repo.Get(ctx, id); s.MonitorPreset != PresetCustom {
		t.Errorf("a request turning season 2 on = %q, want custom", s.MonitorPreset)
	}
}

// Review: after a renumber moved episodes, a season-based preset whose rule no longer
// matches the flags (they travel with each episode) becomes Custom; one that still matches,
// or a file- or date-based preset, is left alone.
func TestRenumberKeepsPresetHonest(t *testing.T) {
	repo, id := presetShow(t)
	ctx := t.Context()
	check := func() string {
		t.Helper()
		if err := store.WithTx(ctx, repo.db, func(tx *store.Transaction) error { return markCustomIfRenumberedTx(ctx, tx, id) }); err != nil {
			t.Fatal(err)
		}
		s, _ := repo.Get(ctx, id)
		return s.MonitorPreset
	}
	if err := repo.ApplyMonitorPreset(ctx, id, PresetFirstSeason, nil, nil); err != nil {
		t.Fatal(err)
	}
	if p := check(); p != PresetFirstSeason {
		t.Fatalf("flags still match the rule, preset = %q", p)
	}
	// An episode that moved into season 2 kept its flag.
	if _, err := repo.db.Exec(`UPDATE episodes SET monitored = 1 WHERE series_id = ? AND season_number = 2 AND episode_number = 1`, id); err != nil {
		t.Fatal(err)
	}
	if p := check(); p != PresetCustom {
		t.Errorf("a moved monitored episode outside the first season = %q, want custom", p)
	}
	if err := repo.ApplyMonitorPreset(ctx, id, PresetMissing, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(`UPDATE episodes SET monitored = 0 WHERE series_id = ?`, id); err != nil {
		t.Fatal(err)
	}
	if p := check(); p != PresetMissing {
		t.Errorf("a file-based preset was judged after a renumber: %q", p)
	}
}

// A request for some seasons of a show already here leaves it Custom; one for the whole
// show is the All preset.
func TestRequestMonitoringPreset(t *testing.T) {
	repo, id := presetShow(t)
	ctx := t.Context()
	if _, err := repo.monitorForRequest(ctx, id, []int{2}, false, true); err != nil {
		t.Fatal(err)
	}
	if s, _ := repo.Get(ctx, id); s.MonitorPreset != PresetCustom || !s.Monitored {
		t.Errorf("some seasons = preset %q, monitored %v; want custom and on", s.MonitorPreset, s.Monitored)
	}
	if _, err := repo.monitorForRequest(ctx, id, nil, true, false); err != nil {
		t.Fatal(err)
	}
	if s, _ := repo.Get(ctx, id); s.MonitorPreset != PresetAll {
		t.Errorf("whole show = preset %q, want all", s.MonitorPreset)
	}
}
