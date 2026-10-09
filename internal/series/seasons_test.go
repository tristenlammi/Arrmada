package series

import (
	"testing"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// SeasonProgress counts each regular season's files, aired episodes and monitored wants,
// lists an announced season with no episodes, and leaves specials out.
func TestSeasonProgress(t *testing.T) {
	d := standardDetails()
	d.Seasons = []metadata.SeasonDetails{
		{SeasonNumber: 0, Episodes: []metadata.EpisodeDetails{{EpisodeNumber: 1, AirDate: "2020-01-01"}}},
		{SeasonNumber: 1, Episodes: []metadata.EpisodeDetails{
			{EpisodeNumber: 1, AirDate: "2020-01-01"}, {EpisodeNumber: 2, AirDate: "2020-01-08"}, {EpisodeNumber: 3, AirDate: "2020-01-15"},
		}},
		{SeasonNumber: 2, Episodes: []metadata.EpisodeDetails{
			{EpisodeNumber: 1, AirDate: "2021-01-01"}, {EpisodeNumber: 2, AirDate: "2999-01-01"}, {EpisodeNumber: 3},
		}},
		{SeasonNumber: 3}, // announced, no episodes yet
	}
	svc, _, ctx := refreshTestService(t, d)
	sr, err := svc.AddWith(ctx, 8, "", AddOptions{Monitored: true, Preset: PresetLatestSeason})
	if err != nil {
		t.Fatal(err)
	}
	// S1 is fully on disk though unmonitored; S2 has its aired episode missing.
	for e := 1; e <= 3; e++ {
		if err := svc.MarkEpisodeImported(ctx, sr.ID, 1, e, "/tv/s1e.mkv", 1); err != nil {
			t.Fatal(err)
		}
	}
	got, err := svc.SeasonProgress(ctx, sr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[0]; ok {
		t.Error("specials are listed")
	}
	want := map[int]SeasonProgress{
		1: {Episodes: 3, Have: 3, Aired: 3},
		2: {Episodes: 3, Aired: 1, Monitored: true, Wanted: 1, Upcoming: 2, MonTotal: 1},
		3: {Monitored: true}, // empty: follows "monitor new seasons", which latest-season sets
	}
	for n, w := range want {
		if got[n] != w {
			t.Errorf("season %d = %+v, want %+v", n, got[n], w)
		}
	}
	if !got[1].OnDisk() || got[2].OnDisk() || got[3].OnDisk() {
		t.Errorf("OnDisk: S1 %v S2 %v S3 %v, want only S1", got[1].OnDisk(), got[2].OnDisk(), got[3].OnDisk())
	}
	if byTMDB, err := svc.GetByTMDB(ctx, 8); err != nil || byTMDB.ID != sr.ID {
		t.Errorf("GetByTMDB = %+v, %v", byTMDB, err)
	}
	if _, err := svc.GetByTMDB(ctx, 999); err != ErrNotFound {
		t.Errorf("GetByTMDB(missing) err = %v, want ErrNotFound", err)
	}
}
