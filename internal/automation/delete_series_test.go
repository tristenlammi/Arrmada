package automation

import (
	"context"
	"testing"

	"github.com/tristenlammi/arrmada/internal/series"
)

// seriesDeleteCoord is removeCoord with a show of two seasons and three in-flight grabs:
// one for season 2, one for season 1, and a pack of both.
func seriesDeleteCoord(t *testing.T) (c *Coordinator, calls *[]removeCall, id int64, s1, s2, pack int64) {
	t.Helper()
	c, calls = removeCoord(t)
	ctx := context.Background()
	repo := series.NewRepo(c.db)
	sr, err := repo.Create(ctx, series.Series{TMDBID: 5, Title: "Harbour", Year: 2020, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertSeasons(ctx, sr.ID, []series.Season{
		{SeasonNumber: 1, Monitored: true, Episodes: []series.Episode{{SeasonNumber: 1, EpisodeNumber: 1, AirDate: "2020-01-01", Monitored: true}}},
		{SeasonNumber: 2, Monitored: true, Episodes: []series.Episode{{SeasonNumber: 2, EpisodeNumber: 1, AirDate: "2021-01-01", Monitored: true}}},
	}); err != nil {
		t.Fatal(err)
	}
	const h1 = "1123456789abcdef0123456789abcdef01234567"
	const h2 = "2123456789abcdef0123456789abcdef01234567"
	const hp = "3123456789abcdef0123456789abcdef01234567"
	s1 = addGrab(t, c, "series", sr.ID, "Harbour.S01.1080p.WEB-DL.x264-GRP", h1)
	s2 = addGrab(t, c, "series", sr.ID, "Harbour.S02.1080p.WEB-DL.x264-GRP", h2)
	pack = addGrab(t, c, "series", sr.ID, "Harbour.S01-S02.1080p.WEB-DL.x264-GRP", hp)
	// Season 2's grab has its scope stamped; the others are read from their names.
	if _, err := c.db.Exec(`UPDATE grabs SET acq_scope = 'S02' WHERE id = ?`, s2); err != nil {
		t.Fatal(err)
	}
	return c, calls, sr.ID, s1, s2, pack
}

// #111: deleting a season cancels only the downloads made for exactly that season: never
// a pack that holds other seasons too, never another season's.
func TestDeleteSeasonCancelsOnlyItsOwnDownloads(t *testing.T) {
	c, calls, id, s1, s2, pack := seriesDeleteCoord(t)
	ctx := context.Background()
	pending, err := c.PendingSeriesDownloads(ctx, id, 2)
	if err != nil || len(pending) != 1 || pending[0].Season != 2 {
		t.Fatalf("season 2's pending = %+v, %v; want its one grab", pending, err)
	}
	if all, _ := c.PendingSeriesDownloads(ctx, id, -1); len(all) != 3 {
		t.Errorf("the show's pending = %d, want 3", len(all))
	}
	if _, err := c.DeleteSeason(ctx, id, 2, DeleteSeasonOpts{Unmonitor: true, CancelDownloads: true}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 1 || (*calls)[0].hash != "2123456789abcdef0123456789abcdef01234567" || !(*calls)[0].deleteData {
		t.Errorf("remove calls = %+v, want only season 2's, with its data", *calls)
	}
	if got := grabStatus(t, c, s2); got != grabStatusCancelled {
		t.Errorf("season 2's grab = %q, want cancelled", got)
	}
	for name, g := range map[string]int64{"season 1": s1, "the pack": pack} {
		if got := grabStatus(t, c, g); got != grabStatusGrabbed {
			t.Errorf("%s's grab = %q, want still grabbed", name, got)
		}
	}
	s, _ := c.series.Get(ctx, id)
	for _, sn := range s.Seasons {
		if want := sn.SeasonNumber != 2; sn.Monitored != want {
			t.Errorf("season %d monitored = %v, want %v", sn.SeasonNumber, sn.Monitored, want)
		}
	}
}

// Without cancel or unmonitor, a season delete leaves the downloads exactly as they were
// (still in flight, so the sweep doesn't grab a second copy) and the season wanted.
func TestDeleteSeasonLeavesDownloadsUnlessAsked(t *testing.T) {
	c, calls, id, _, s2, _ := seriesDeleteCoord(t)
	ctx := context.Background()
	if _, err := c.DeleteSeason(ctx, id, 2, DeleteSeasonOpts{}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 0 {
		t.Errorf("remove calls = %+v, want none", *calls)
	}
	if got := grabStatus(t, c, s2); got != grabStatusGrabbed {
		t.Errorf("season 2's grab = %q, want still grabbed", got)
	}
	s, _ := c.series.Get(ctx, id)
	for _, sn := range s.Seasons {
		if !sn.Monitored {
			t.Errorf("season %d was unmonitored", sn.SeasonNumber)
		}
	}
}

// Deleting the show cancels every one of its downloads when asked.
func TestDeleteSeriesCancelsItsDownloads(t *testing.T) {
	c, calls, id, s1, s2, pack := seriesDeleteCoord(t)
	if _, err := c.DeleteSeries(context.Background(), id, DeleteSeriesOpts{CancelDownloads: true}); err != nil {
		t.Fatal(err)
	}
	if len(*calls) != 3 {
		t.Errorf("remove calls = %+v, want all three", *calls)
	}
	for _, g := range []int64{s1, s2, pack} {
		if got := grabStatus(t, c, g); got != grabStatusCancelled {
			t.Errorf("grab %d = %q, want cancelled", g, got)
		}
	}
}
