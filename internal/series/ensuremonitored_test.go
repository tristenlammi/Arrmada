package series

import (
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// monitoredSeasons reads each regular season's flag and how many of its episodes are
// monitored, plus whether specials are.
func monitoredSeasons(t *testing.T, svc *Service, id int64) (seasons map[int]bool, eps map[int]int) {
	t.Helper()
	sr, err := svc.Get(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	seasons, eps = map[int]bool{}, map[int]int{}
	for _, sn := range sr.Seasons {
		seasons[sn.SeasonNumber] = sn.Monitored
		for _, e := range sn.Episodes {
			if e.Monitored {
				eps[sn.SeasonNumber]++
			}
		}
	}
	return seasons, eps
}

func withSpecials(seasons []metadata.SeasonDetails) []metadata.SeasonDetails {
	return append([]metadata.SeasonDetails{{SeasonNumber: 0, Episodes: []metadata.EpisodeDetails{{EpisodeNumber: 1, AirDate: "2020-01-01"}}}}, seasons...)
}

// Adding a show for some seasons monitors exactly those seasons and their episodes —
// never specials — and leaves "monitor new seasons" off; an announced season with no
// episodes yet that was asked for is monitored too.
func TestAddWithOptionsMonitorsOnlyChosenSeasons(t *testing.T) {
	d := standardDetails()
	d.Seasons = withSpecials(append(listing(2, 2, 2), metadata.SeasonDetails{SeasonNumber: 4}))
	svc, _, ctx := refreshTestService(t, d)
	sr, err := svc.AddWith(ctx, 8, "", AddOptions{Monitored: true, Preset: PresetAll, Seasons: map[int]bool{1: true, 2: true, 4: true, 0: true}})
	if err != nil {
		t.Fatal(err)
	}
	if !sr.Monitored || sr.MonitorNewSeasons {
		t.Errorf("gate %v, monitor new seasons %v; want on, off", sr.Monitored, sr.MonitorNewSeasons)
	}
	seasons, eps := monitoredSeasons(t, svc, sr.ID)
	want := map[int]bool{0: false, 1: true, 2: true, 3: false, 4: true}
	for n, w := range want {
		if seasons[n] != w {
			t.Errorf("season %d monitored = %v, want %v", n, seasons[n], w)
		}
	}
	if eps[0] != 0 || eps[1] != 2 || eps[2] != 2 || eps[3] != 0 {
		t.Errorf("monitored episodes by season = %v, want S1 2, S2 2, none else", eps)
	}
}

// EnsureMonitored turns on just the asked-for season of a show the owner already
// monitors, without touching other seasons' choices or the new-season setting.
func TestEnsureMonitoredDoesNotCascade(t *testing.T) {
	d := standardDetails()
	d.Seasons = withSpecials(listing(2, 2, 2, 2))
	svc, _, ctx := refreshTestService(t, d)
	sr, err := svc.AddWith(ctx, 8, "", AddOptions{Monitored: true, Seasons: map[int]bool{1: true}})
	if err != nil {
		t.Fatal(err)
	}
	// The owner also wants one episode of S2, and new seasons.
	full, _ := svc.Get(ctx, sr.ID)
	if err := svc.SetEpisodeMonitored(ctx, full.Seasons[2].Episodes[0].ID, true); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetMonitorNewSeasons(ctx, sr.ID, true); err != nil {
		t.Fatal(err)
	}

	if err := svc.EnsureMonitored(ctx, sr.ID, []int{4, 0}, "alice"); err != nil {
		t.Fatal(err)
	}
	seasons, eps := monitoredSeasons(t, svc, sr.ID)
	if !seasons[1] || !seasons[2] || seasons[3] || !seasons[4] || seasons[0] {
		t.Errorf("season flags = %v, want S1, S2, S4", seasons)
	}
	if eps[1] != 2 || eps[2] != 1 || eps[3] != 0 || eps[4] != 2 || eps[0] != 0 {
		t.Errorf("monitored episodes = %v, want S1 2, S2 1 (the owner's pick), S4 2", eps)
	}
	got, _ := svc.Get(ctx, sr.ID)
	if !got.Monitored || !got.MonitorNewSeasons {
		t.Errorf("gate %v, new seasons %v; want both still on", got.Monitored, got.MonitorNewSeasons)
	}
	evs, _ := svc.Events(ctx, sr.ID, 10)
	if len(evs) == 0 || !strings.Contains(evs[0].Detail, "Monitored by request from alice: S4") {
		t.Errorf("latest event = %+v, want the request named", evs)
	}
}

// A paused show resumed for one requested season fetches only that season: what the
// owner had paused stays off instead of resuming with it, and new seasons stay off.
func TestEnsureMonitoredPausedShowOnlyRequestedSeasons(t *testing.T) {
	d := standardDetails()
	d.Seasons = listing(2, 2, 2)
	svc, _, ctx := refreshTestService(t, d)
	sr, err := svc.AddWith(ctx, 8, "", AddOptions{Monitored: false, Preset: PresetAll})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureMonitored(ctx, sr.ID, []int{3}, "bob"); err != nil {
		t.Fatal(err)
	}
	seasons, eps := monitoredSeasons(t, svc, sr.ID)
	if seasons[1] || seasons[2] || !seasons[3] || eps[1] != 0 || eps[2] != 0 || eps[3] != 2 {
		t.Errorf("flags %v episodes %v, want only S3", seasons, eps)
	}
	got, _ := svc.Get(ctx, sr.ID)
	if !got.Monitored || got.MonitorNewSeasons {
		t.Errorf("gate %v, new seasons %v; want on, off", got.Monitored, got.MonitorNewSeasons)
	}
	evs, _ := svc.Events(ctx, sr.ID, 10)
	if len(evs) == 0 || !strings.Contains(evs[0].Detail, "other seasons are no longer monitored") {
		t.Errorf("latest event = %+v, want it to say the paused seasons were turned off", evs)
	}
}

// A whole-show request monitors every regular season (a library-scanned show had none)
// and new seasons, never specials.
func TestEnsureMonitoredWholeShow(t *testing.T) {
	d := standardDetails()
	d.Seasons = withSpecials(listing(2, 2))
	svc, _, ctx := refreshTestService(t, d)
	sr, err := svc.Add(ctx, 8, "", false) // as a library scan adds it
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.EnsureMonitored(ctx, sr.ID, nil, "carol"); err != nil {
		t.Fatal(err)
	}
	seasons, eps := monitoredSeasons(t, svc, sr.ID)
	if seasons[0] || !seasons[1] || !seasons[2] || eps[0] != 0 || eps[1] != 2 || eps[2] != 2 {
		t.Errorf("flags %v episodes %v, want S1-2 whole, no specials", seasons, eps)
	}
	got, _ := svc.Get(ctx, sr.ID)
	if !got.Monitored || !got.MonitorNewSeasons {
		t.Errorf("gate %v, new seasons %v; want both on", got.Monitored, got.MonitorNewSeasons)
	}
}

func TestSeasonsLabel(t *testing.T) {
	for in, want := range map[string][]int{
		"S1–3, S5": {3, 1, 2, 5, 0},
		"S4":       {4},
		"S1, S3":   {1, 3, 3},
		"":         nil,
	} {
		if got := SeasonsLabel(want); got != in {
			t.Errorf("SeasonsLabel(%v) = %q, want %q", want, got, in)
		}
	}
}
