package automation

import (
	"context"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// latestAttempt is the newest stored attempt for a title.
func latestAttempt(t *testing.T, h *stallHarness, kind string, id int64) Attempt {
	t.Helper()
	got, err := h.c.SearchAttempts(h.ctx, kind, id, 0, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatalf("no %s attempt recorded for %d", kind, id)
	}
	return got[0]
}

func attemptCount(t *testing.T, h *stallHarness, kind string, id int64) int {
	t.Helper()
	var n int
	if err := h.c.db.QueryRow(`SELECT COUNT(*) FROM search_attempts WHERE media_type = ? AND media_id = ?`, kind, id).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A search that found releases and took none says where they went: other films, and the
// profile's bitrate ceiling — counted by the quality engine's own reject codes.
func TestSearchOutcomeCountsReasons(t *testing.T) {
	h := newStallHarness(t)
	createDefault(t, h.c.quality, quality.StoredProfile{MediaType: quality.MediaMovie, Name: "Capped", BitrateCapMbps: 10})
	mid := h.addMovie(t, 1, "Arrival", 2016)
	// 4 GiB over 20 minutes is ~29 Mb/s: over the 10 Mb/s ceiling.
	if _, err := h.c.db.Exec(`UPDATE movies SET runtime = 20 WHERE id = ?`, mid); err != nil {
		t.Fatal(err)
	}
	h.ix.offer(
		"Arrival.2016.1080p.BluRay.x264-GRP", "Arrival.2016.2160p.WEB-DL.x265-GRP",
		"Inception.2010.1080p.BluRay.x264-GRP", "Interstellar.2014.1080p.WEB-DL.x264-GRP", "Tenet.2020.1080p.WEB-DL.x264-GRP",
	)
	out, err := h.c.SearchMovie(WithSearchTrigger(h.ctx, TriggerManual), mid)
	if err != nil {
		t.Fatal(err)
	}
	if out.WrongTitle != 3 || out.Rejected != 2 || out.Reasons[quality.RejectBitrateCeiling] != 2 || out.AttemptID == 0 {
		t.Fatalf("outcome = %+v", out)
	}
	a := latestAttempt(t, h, AttemptMovie, mid)
	if a.Returned != 5 || a.WrongTitle != 3 || a.Rejected != 2 || a.Eligible != 0 || a.Grabbed != 0 {
		t.Fatalf("attempt counts = %+v", a)
	}
	if a.Reasons[DropWrongTitle] != 3 || a.Reasons[quality.RejectBitrateCeiling] != 2 {
		t.Fatalf("reasons = %v", a.Reasons)
	}
	if a.TopReason != DropWrongTitle || a.Example == "" {
		t.Fatalf("top reason = %q, example %q", a.TopReason, a.Example)
	}
	if a.Outcome != OutcomeNoneSuitable || a.Trigger != TriggerManual || a.Scope != "" || a.ID != out.AttemptID {
		t.Fatalf("attempt = %+v", a)
	}
	if h.adds() != 0 {
		t.Fatal("a release over the ceiling was grabbed")
	}
}

func TestSearchAttemptCountsBlocklistedAndGrabs(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	blocked := "Arrival.2016.2160p.WEB-DL.x265-BAD"
	h.ix.offer(blocked, arrivalRelease, "Inception.2010.1080p.BluRay.x264-GRP")
	if err := h.c.Blocklist(h.ctx, mid, blocked, "Fake", "", "test"); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SearchMovie(h.ctx, mid); err != nil {
		t.Fatal(err)
	}
	a := latestAttempt(t, h, AttemptMovie, mid)
	if a.Outcome != OutcomeGrabbed || a.Grabbed != 1 || len(a.GrabbedTitles) != 1 || a.GrabbedTitles[0] != arrivalRelease {
		t.Fatalf("attempt = %+v", a)
	}
	if a.Blocklisted != 1 || a.WrongTitle != 1 || a.Eligible != 1 || a.Returned != 3 {
		t.Fatalf("counts = %+v", a)
	}
	if a.Trigger != TriggerOther {
		t.Fatalf("trigger = %q, want %q for a search with none set", a.Trigger, TriggerOther)
	}
}

// A search nobody could answer records indexers_failed with each indexer's error, and the
// sweep still counts no miss.
func TestAllIndexersFailedRecordsAttemptNotMiss(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	if _, err := h.c.db.Exec(`UPDATE movies SET min_availability = 'announced' WHERE id = ?`, mid); err != nil {
		t.Fatal(err)
	}
	h.ix.mu.Lock()
	h.ix.down = true
	h.ix.mu.Unlock()
	h.c.SearchMissing(h.ctx)
	a := latestAttempt(t, h, AttemptMovie, mid)
	if a.Outcome != OutcomeIndexersFailed || a.Reason != ReasonIndexersFailed || a.Trigger != TriggerSweep {
		t.Fatalf("attempt = %+v", a)
	}
	if a.IndexerErrors["Fake"] == "" {
		t.Fatalf("indexer errors = %v", a.IndexerErrors)
	}
	if _, misses := h.c.movies.SearchState(h.ctx, mid); misses != 0 {
		t.Fatalf("misses = %d, an outage must not count", misses)
	}
}

// Nothing wanted means no search ran: nothing is stored.
func TestNothingWantedRecordsNoAttempt(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	if _, err := h.c.db.Exec(`UPDATE movies SET has_file = 1, movie_file_path = '/library/Arrival (2016)/Arrival.mkv' WHERE id = ?`, mid); err != nil {
		t.Fatal(err)
	}
	if _, err := h.c.SearchMovie(h.ctx, mid); err != nil {
		t.Fatal(err)
	}
	if n := attemptCount(t, h, AttemptMovie, mid); n != 0 {
		t.Fatalf("%d attempts stored for a search that never ran", n)
	}
}

func TestRecordSearchAttemptPrunes(t *testing.T) {
	h := newStallHarness(t)
	for i := 0; i < maxAttemptsPerTitle+1; i++ {
		out := SearchOutcome{Searched: true, Returned: i}
		h.c.recordAttempt(h.ctx, nil, AttemptMovie, 7, "", &out, nil)
	}
	if n := attemptCount(t, h, AttemptMovie, 7); n != maxAttemptsPerTitle {
		t.Fatalf("%d attempts kept, want %d", n, maxAttemptsPerTitle)
	}
	got, _ := h.c.SearchAttempts(h.ctx, AttemptMovie, 7, 0, 50)
	if got[0].Returned != maxAttemptsPerTitle || got[len(got)-1].Returned != 1 {
		t.Fatalf("kept the wrong rows: newest returned=%d, oldest returned=%d", got[0].Returned, got[len(got)-1].Returned)
	}
	// Another title's rows are untouched by the pruning.
	other := SearchOutcome{Searched: true}
	h.c.recordAttempt(h.ctx, nil, AttemptSeries, 7, "", &other, nil)
	if n := attemptCount(t, h, AttemptMovie, 7); n != maxAttemptsPerTitle {
		t.Fatalf("a series attempt pruned the movie's: %d", n)
	}
}

func TestSearchFinishedEventCarriesIdsAndCounts(t *testing.T) {
	h := newStallHarness(t)
	events, cancel := h.c.bus.Subscribe("search.finished")
	defer cancel()
	mid := h.addMovie(t, 1, "Arrival", 2016)
	h.ix.offer(arrivalRelease)
	if _, err := h.c.SearchMovie(h.ctx, mid); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-events:
		p, _ := ev.Data.(map[string]any)
		if p["media_type"] != AttemptMovie || p["media_id"] != mid || p["outcome"] != OutcomeGrabbed || p["grabbed"] != 1 {
			t.Fatalf("payload = %v", p)
		}
		for _, k := range []string{"grabbed_titles", "example", "title"} {
			if _, ok := p[k]; ok {
				t.Fatalf("payload carries %q; it should be ids and counts only: %v", k, p)
			}
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no search.finished event")
	}
}

// The summary the Wanted view reads: latest attempt, empty tries since the last grab, and
// the reason most often on top. Upgrade searches stay out of it.
func TestLatestAttemptsSummarises(t *testing.T) {
	h := newStallHarness(t)
	rec := func(id int64, scope string, out SearchOutcome, err error) {
		h.c.recordAttempt(h.ctx, nil, AttemptMovie, id, scope, &out, err)
	}
	rec(1, "", SearchOutcome{Searched: true, Grabbed: 1, Returned: 3}, nil)
	rec(1, "", SearchOutcome{Searched: true, Returned: 9, TopReason: quality.RejectBitrateCeiling}, nil)
	rec(1, "", SearchOutcome{Searched: true, Returned: 0}, nil)
	rec(1, "", SearchOutcome{Searched: true, Reason: ReasonIndexersFailed}, nil)
	rec(1, "", SearchOutcome{Searched: true, Returned: 4, TopReason: quality.RejectBitrateCeiling}, nil)
	rec(1, ScopeUpgrade, SearchOutcome{Searched: true, Returned: 2}, nil)
	rec(2, ScopeUpgrade, SearchOutcome{Searched: true, Returned: 2}, nil)

	got, err := h.c.LatestAttempts(h.ctx, AttemptMovie, []int64{1, 2, 3})
	if err != nil {
		t.Fatal(err)
	}
	s, ok := got[1]
	if !ok {
		t.Fatal("no summary for movie 1")
	}
	if s.Latest.Returned != 4 || s.Latest.Scope != "" {
		t.Fatalf("latest = %+v, want the newest non-upgrade attempt", s.Latest)
	}
	if s.EmptyTries != 3 || s.MainReason != quality.RejectBitrateCeiling {
		t.Fatalf("summary = %+v, want 3 empty tries (the outage neither counts nor breaks the run)", s)
	}
	if _, ok := got[2]; ok {
		t.Fatal("a title with only upgrade searches has a missing-search summary")
	}
	if _, ok := got[3]; ok {
		t.Fatal("a title never searched has a summary")
	}
}

func TestSeriesSearchAttemptAggregatesPasses(t *testing.T) {
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
	// The broad and the per-season query both return these; each is counted once.
	h.ix.offer("Show.S01E01.1080p.WEB-DL.x264-GRP", "Show.Mediterranean.S01E01.1080p.WEB-DL.x264-GRP")
	out, err := h.c.SearchSeriesNow(h.ctx, sr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if out.Grabbed != 1 || len(out.GrabbedTitles) != 1 {
		t.Fatalf("outcome = %+v", out)
	}
	a := latestAttempt(t, h, AttemptSeries, sr.ID)
	if a.Outcome != OutcomeGrabbed || a.WrongTitle != 1 || a.Reasons[DropWrongTitle] != 1 || a.GrabbedTitles[0] != "Show.S01E01.1080p.WEB-DL.x264-GRP" {
		t.Fatalf("attempt = %+v", a)
	}

	// A season click is its own attempt, under its scope.
	if _, err := h.c.GrabForScope(WithSearchTrigger(context.Background(), TriggerManual), sr.ID, SeriesScope{Season: 1, Episode: 1}); err != nil {
		t.Fatal(err)
	}
	a = latestAttempt(t, h, AttemptSeries, sr.ID)
	if a.Scope != "S01E01" || a.Trigger != TriggerManual {
		t.Fatalf("scoped attempt = %+v", a)
	}
	// The release just grabbed is now pending: the click says so rather than grabbing it twice.
	if a.Pending != 1 || a.Outcome != OutcomeNoneSuitable {
		t.Fatalf("scoped attempt counts = %+v", a)
	}
}
