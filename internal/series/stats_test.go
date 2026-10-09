package series

import (
	"testing"
)

// statsShow: season 1 (unmonitored) aired with no files; season 2 (monitored) has two
// aired episodes with files, one aired without, an unmonitored aired one, and two still to
// air; a special is missing too.
func statsShow(t *testing.T) (*Repo, int64) {
	t.Helper()
	repo, ctx := testRepo(t)
	sr, err := repo.Create(ctx, Series{TMDBID: 1, Title: "Show", Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	ep := func(s, e int, air string, mon bool) Episode {
		return Episode{SeasonNumber: s, EpisodeNumber: e, AirDate: air, Monitored: mon}
	}
	if err := repo.InsertSeasons(ctx, sr.ID, []Season{
		{SeasonNumber: 0, Monitored: true, Episodes: []Episode{ep(0, 1, "2020-01-01", true)}},
		{SeasonNumber: 1, Monitored: false, Episodes: []Episode{ep(1, 1, "2020-01-01", false), ep(1, 2, "2020-01-08", false), ep(1, 3, "2020-01-15", true)}},
		{SeasonNumber: 2, Monitored: true, Episodes: []Episode{
			ep(2, 1, "2021-01-01", true), ep(2, 2, "2021-01-08", true), ep(2, 3, "2021-01-15", true),
			ep(2, 4, "2021-01-22", false), ep(2, 5, "2999-03-01", true), ep(2, 6, "2999-01-01", false),
		}},
	}); err != nil {
		t.Fatal(err)
	}
	_ = repo.SetEpisodeFile(ctx, sr.ID, 2, 1, "/a.mkv", 10)
	_ = repo.SetEpisodeFile(ctx, sr.ID, 2, 2, "/b.mkv", 5)
	return repo, sr.ID
}

// Only monitored episodes in monitored seasons count as wanted; files always count.
func TestAllStatsIgnoresUnmonitoredSeasons(t *testing.T) {
	repo, id := statsShow(t)
	all, err := repo.allStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	st := all[id]
	if st == nil {
		t.Fatal("no stats for the show")
	}
	// episodes: 2 files + S02E03 (wanted, aired) = 3; season 1 and S02E04 aren't wanted.
	if st.Episodes != 3 || st.HaveFiles != 2 || st.Missing != 1 || st.SizeBytes != 15 {
		t.Errorf("stats = %+v, want episodes 3, have 2, missing 1, size 15", st)
	}
	// S01E01-03 (season off) and S02E04 (episode off) are aired, file-less, not wanted.
	if st.UnmonitoredMissing != 4 {
		t.Errorf("unmonitored_missing = %d, want 4", st.UnmonitoredMissing)
	}
	if st.Seasons != 2 {
		t.Errorf("seasons = %d, want 2 (specials left out)", st.Seasons)
	}

	// The latest season complete and the rest unmonitored reads Complete: nothing missing.
	_ = repo.SetEpisodeFile(t.Context(), id, 2, 3, "/c.mkv", 1)
	all, _ = repo.allStats(t.Context())
	if st := all[id]; st.Missing != 0 || st.HaveFiles != st.Episodes {
		t.Errorf("after filling season 2: %+v, want nothing missing and have == episodes", st)
	}
}

func TestStatsForMatchesAllStats(t *testing.T) {
	repo, id := statsShow(t)
	// A second show, so the single-series filter has something to leave out.
	other, _ := repo.Create(t.Context(), Series{TMDBID: 2, Title: "Other", Monitored: true})
	_ = repo.InsertSeasons(t.Context(), other.ID, []Season{{SeasonNumber: 1, Monitored: true, Episodes: []Episode{{SeasonNumber: 1, EpisodeNumber: 1, AirDate: "2020-01-01", Monitored: true}}}})
	all, err := repo.allStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	one, err := repo.StatsFor(t.Context(), id)
	if err != nil {
		t.Fatal(err)
	}
	if *one != *all[id] {
		t.Errorf("StatsFor = %+v, allStats = %+v", one, all[id])
	}
	if empty, err := repo.StatsFor(t.Context(), 999); err != nil || empty.Episodes != 0 {
		t.Errorf("an unknown show: %+v, %v", empty, err)
	}
}

// The next air date is the soonest monitored episode still to air, not just any.
func TestNextAirDateMonitoredOnly(t *testing.T) {
	repo, id := statsShow(t)
	st, _ := repo.StatsFor(t.Context(), id)
	if st.NextAirDate != "2999-03-01" {
		t.Errorf("next_air_date = %q, want the monitored S02E05 (S02E06 is earlier but unmonitored)", st.NextAirDate)
	}
}
