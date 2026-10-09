package automation

import (
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/series"
)

// The Search button on a movie that's already downloading says so and asks no indexer
// (ACQ-19): a click mid-download used to be able to grab a second copy.
func TestSearchMovieManualSkipsInFlight(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	busy := "Arrival.2016.2160p.WEB-DL.x265-GRP"
	h.qbit.stalledTorrent(hashFor(busy), busy, "arrmada")
	h.ix.offer(arrivalRelease)

	out, err := h.c.SearchMovieManual(WithSearchTrigger(h.ctx, TriggerManual), mid)
	if err != nil {
		t.Fatal(err)
	}
	if out.Reason != ReasonAlreadyDownloading || out.Example != busy || out.Grabbed != 0 {
		t.Fatalf("outcome = %+v", out)
	}
	if n := h.ix.searchCount(); n != 0 {
		t.Fatalf("the indexer was asked %d times for a movie already downloading", n)
	}
	if msg := out.Message("movie"); msg != "Already downloading "+busy {
		t.Fatalf("message = %q", msg)
	}
	a := latestAttempt(t, h, AttemptMovie, mid)
	if a.Outcome != OutcomeSkippedInFlight || a.Example != busy || a.Trigger != TriggerManual {
		t.Fatalf("attempt = %+v", a)
	}
	// A second click while it's still downloading doesn't add another identical row.
	if _, err := h.c.SearchMovieManual(h.ctx, mid); err != nil {
		t.Fatal(err)
	}
	if n := attemptCount(t, h, AttemptMovie, mid); n != 1 {
		t.Fatalf("%d attempts for two identical in-flight skips", n)
	}
	// A re-search after a block isn't stopped by the torrent still listed.
	if out, err := h.c.SearchMovie(h.ctx, mid); err != nil || out.Grabbed != 1 {
		t.Fatalf("SearchMovie = %+v, %v; want it to search and grab", out, err)
	}
}

// The movie search's outcome counts match what the indexer offered.
func TestSearchMovieManualCountsOutcome(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	blocked := "Arrival.2016.2160p.WEB-DL.x265-BAD"
	h.ix.offer(blocked, "Inception.2010.1080p.BluRay.x264-GRP", "Tenet.2020.1080p.WEB-DL.x264-GRP")
	if err := h.c.Blocklist(h.ctx, mid, blocked, "Fake", "", "test"); err != nil {
		t.Fatal(err)
	}
	out, err := h.c.SearchMovieManual(h.ctx, mid)
	if err != nil {
		t.Fatal(err)
	}
	if out.Returned != 3 || out.WrongTitle != 2 || out.Blocklisted != 1 || out.Grabbed != 0 || out.Reason != ReasonBlockedOrBelow {
		t.Fatalf("outcome = %+v", out)
	}
}

// Two searches of one movie at once: the second is turned away, not run (run with -race).
func TestConcurrentManualSearchesOneRuns(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	release, ok := h.c.claims.claim(movieKey(mid)) // the first search, mid-flight
	if !ok {
		t.Fatal("claim")
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i := range errs {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, errs[i] = h.c.SearchMovieManual(h.ctx, mid)
		}(i)
	}
	wg.Wait()
	release()
	for _, err := range errs {
		if err != ErrAlreadySearching {
			t.Fatalf("errs = %v, want both turned away while the first runs", errs)
		}
	}
	if n := attemptCount(t, h, AttemptMovie, mid); n != 0 {
		t.Fatalf("a turned-away search stored %d attempts", n)
	}
}

// The series Search button holds back what's downloading, like the sweep.
func TestSearchSeriesManualSkipsWholeShowInFlight(t *testing.T) {
	h := newStallHarness(t)
	repo := series.NewRepo(h.c.db)
	sr, err := repo.Create(h.ctx, series.Series{TMDBID: 9, Title: "Show", Year: 2020, Monitored: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := repo.InsertSeasons(h.ctx, sr.ID, []series.Season{{SeasonNumber: 1, Monitored: true, Episodes: []series.Episode{
		{SeasonNumber: 1, EpisodeNumber: 1, AirDate: "2021-01-01", Monitored: true},
	}}}); err != nil {
		t.Fatal(err)
	}
	busy := "Show.S01.1080p.WEB-DL.x264-GRP"
	h.qbit.stalledTorrent(hashFor(busy), busy, seriesCategory)
	h.ix.offer("Show.S01E01.1080p.WEB-DL.x264-GRP")

	out, err := h.c.SearchSeriesManual(h.ctx, sr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Reason != ReasonAlreadyDownloading || out.Example != busy || h.ix.searchCount() != 0 {
		t.Fatalf("outcome = %+v, searches = %d", out, h.ix.searchCount())
	}
	if a := latestAttempt(t, h, AttemptSeries, sr.ID); a.Outcome != OutcomeSkippedInFlight {
		t.Fatalf("attempt = %+v", a)
	}
}
