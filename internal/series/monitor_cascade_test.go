package series

import (
	"context"
	"database/sql"
	"testing"

	"github.com/tristenlammi/arrmada/internal/store"
)

func monitorFixture(t *testing.T, seriesMonitored int) (*Repo, *sql.DB, context.Context) {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB()
	ctx := context.Background()
	if _, err := db.ExecContext(ctx, `INSERT INTO series (id,tmdb_id,title,monitored) VALUES (1,7,'Dandadan',?)`, seriesMonitored); err != nil {
		t.Fatal(err)
	}
	for _, sn := range []int{0, 1, 2, 3, 4} {
		_, _ = db.ExecContext(ctx, `INSERT INTO seasons (series_id,season_number,monitored) VALUES (1,?,0)`, sn)
		_, _ = db.ExecContext(ctx, `INSERT INTO episodes (series_id,season_number,episode_number,air_date,monitored) VALUES (1,?,1,'2024-10-04',0)`, sn)
	}
	return NewRepo(db), db, ctx
}

func countEps(t *testing.T, db *sql.DB, where string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM episodes WHERE series_id = 1 AND ` + where).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The series switch is a gate. Pausing and resuming used to cascade to every season and
// episode, re-monitoring the seasons the owner had switched off — and the next sweep
// grabbed them.
func TestPauseResumeKeepsSeasonChoices(t *testing.T) {
	repo, db, ctx := monitorFixture(t, 1)
	// Only season 4 is wanted; 1-3 are off.
	if err := repo.SetSeasonMonitored(ctx, 1, 4, true); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetMonitored(ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if got := countEps(t, db, `season_number = 4 AND monitored = 1`); got != 1 {
		t.Errorf("pausing switched season 4's episode off (%d monitored)", got)
	}
	if err := repo.SetMonitored(ctx, 1, true); err != nil {
		t.Fatal(err)
	}
	if got := countEps(t, db, `season_number BETWEEN 1 AND 3 AND monitored = 1`); got != 0 {
		t.Errorf("resuming re-monitored %d episodes in seasons 1-3", got)
	}
	flags, _ := repo.SeasonMonitorFlags(ctx, 1)
	if flags[1] || flags[2] || flags[3] || !flags[4] {
		t.Errorf("season flags after pause/resume = %v, want only 4", flags)
	}
	if sr, _ := repo.Get(ctx, 1); !sr.Monitored {
		t.Error("the series should be monitored again")
	}
}

// A library-scanned show is added with nothing monitored; turning it on must still
// monitor every regular episode (and new seasons), or it reads "Monitored" and never
// grabs anything. Specials stay out of it.
func TestEnableWithNothingMonitoredMonitorsAllRegular(t *testing.T) {
	repo, db, ctx := monitorFixture(t, 0)
	if err := repo.SetMonitorNewSeasons(ctx, 1, false); err != nil {
		t.Fatal(err)
	}
	if err := repo.SetMonitored(ctx, 1, true); err != nil {
		t.Fatal(err)
	}
	if got := countEps(t, db, `season_number > 0 AND monitored = 1`); got != 4 {
		t.Errorf("regular episodes monitored = %d, want 4", got)
	}
	if got := countEps(t, db, `season_number = 0 AND monitored = 1`); got != 0 {
		t.Errorf("specials monitored = %d, want 0", got)
	}
	flags, _ := repo.SeasonMonitorFlags(ctx, 1)
	if flags[0] || !flags[1] || !flags[4] {
		t.Errorf("season flags = %v, want every regular season on", flags)
	}
	if sr, _ := repo.Get(ctx, 1); !sr.MonitorNewSeasons {
		t.Error("turning on a show with nothing monitored should monitor new seasons too")
	}
}

// A season new on refresh follows "monitor new seasons"; a new episode in an existing
// season takes that season's flag — whatever the series switch says.
func TestRefreshNewSeasonFollowsMonitorNewSeasons(t *testing.T) {
	for _, mns := range []bool{true, false} {
		d := standardDetails()
		d.Seasons = listing(2)
		svc, fm, ctx := refreshTestService(t, d)
		sr, err := svc.Add(ctx, d.TMDBID, "", true)
		if err != nil {
			t.Fatal(err)
		}
		if err := svc.SetMonitorNewSeasons(ctx, sr.ID, mns); err != nil {
			t.Fatal(err)
		}
		fm.d.Seasons = listing(2, 1)
		if _, _, err := svc.Refresh(ctx, sr.ID, RefreshOptions{}); err != nil {
			t.Fatal(err)
		}
		got, _ := svc.Get(ctx, sr.ID)
		for _, sn := range got.Seasons {
			if sn.SeasonNumber != 2 {
				continue
			}
			if sn.Monitored != mns || len(sn.Episodes) != 1 || sn.Episodes[0].Monitored != mns {
				t.Errorf("monitor_new_seasons=%v: new season 2 = %+v", mns, sn)
			}
		}
	}
}

func TestRefreshNewEpisodeInheritsSeasonFlag(t *testing.T) {
	d := standardDetails()
	d.Seasons = listing(2, 2)
	svc, fm, ctx := refreshTestService(t, d)
	sr, err := svc.Add(ctx, d.TMDBID, "", true)
	if err != nil {
		t.Fatal(err)
	}
	// Season 1 off; then pause the show — the pause must not change what new rows get.
	if err := svc.SetSeasonMonitored(ctx, sr.ID, 1, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.SetMonitored(ctx, sr.ID, false); err != nil {
		t.Fatal(err)
	}
	fm.d.Seasons = listing(3, 3)
	if _, _, err := svc.Refresh(ctx, sr.ID, RefreshOptions{}); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Get(ctx, sr.ID)
	for _, sn := range got.Seasons {
		for _, e := range sn.Episodes {
			if e.EpisodeNumber != 3 {
				continue
			}
			want := sn.SeasonNumber == 2
			if e.Monitored != want {
				t.Errorf("new S%02dE03 monitored = %v, want %v (its season's flag)", sn.SeasonNumber, e.Monitored, want)
			}
		}
	}
}
