package automation

import (
	"bytes"
	"log/slog"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/series"
)

// The blocklist and the pending-grab list are safety reads: when either can't be read,
// nothing is grabbed that round and the log says why. Reading them as empty instead made
// every blocklisted fake grabbable again the moment the database hiccuped.

// logTo points the harness' coordinator log at a buffer.
func (h *stallHarness) logTo() *bytes.Buffer {
	var buf bytes.Buffer
	h.c.log = slog.New(slog.NewTextHandler(&buf, nil))
	return &buf
}

func (h *stallHarness) adds() int {
	n := 0
	for _, c := range h.qbit.callLog() {
		if strings.HasPrefix(c, "add ") {
			n++
		}
	}
	return n
}

const arrivalRelease = "Arrival.2016.1080p.BluRay.x264-GRP"

// Control: with both lists readable the release is grabbed, so the failures below are
// down to the unreadable list and nothing else.
func TestSearchGrabsWhenSafetyReadsWork(t *testing.T) {
	h := newStallHarness(t)
	mid := h.addMovie(t, 1, "Arrival", 2016)
	h.ix.offer(arrivalRelease)
	if _, err := h.c.SearchMovie(h.ctx, mid); err != nil {
		t.Fatal(err)
	}
	if h.adds() != 1 {
		t.Fatalf("client calls = %q, want one add", h.qbit.callLog())
	}
}

func TestUnreadableBlocklistGrabsNothing(t *testing.T) {
	h := newStallHarness(t)
	logs := h.logTo()
	mid := h.addMovie(t, 1, "Arrival", 2016)
	h.ix.offer(arrivalRelease)
	if _, err := h.c.db.Exec(`DROP TABLE blocklist`); err != nil {
		t.Fatal(err)
	}

	if _, err := h.c.SearchMovie(h.ctx, mid); err == nil {
		t.Error("search with an unreadable blocklist reported success")
	}
	if n := h.adds(); n != 0 {
		t.Fatalf("%d release(s) handed to the client with the blocklist unreadable", n)
	}
	if !strings.Contains(logs.String(), "blocklist unreadable — not grabbing Arrival this round") {
		t.Errorf("no warning naming the title; log:\n%s", logs.String())
	}

	// RSS takes the same path per movie.
	logs.Reset()
	h.c.RSSSync(h.ctx)
	if n := h.adds(); n != 0 {
		t.Fatalf("RSS grabbed %d release(s) with the blocklist unreadable", n)
	}

	// Interactive ranking fails rather than presenting releases as grabbable.
	if _, err := h.c.RankReleases(h.ctx, mid); err == nil {
		t.Error("ranking with an unreadable blocklist succeeded")
	}
}

func TestUnreadablePendingGrabsGrabsNothing(t *testing.T) {
	h := newStallHarness(t)
	logs := h.logTo()
	mid := h.addMovie(t, 1, "Arrival", 2016)
	h.ix.offer(arrivalRelease)
	if _, err := h.c.db.Exec(`ALTER TABLE grabs RENAME TO grabs_unreadable`); err != nil {
		t.Fatal(err)
	}

	_, _ = h.c.SearchMovie(h.ctx, mid)
	if n := h.adds(); n != 0 {
		t.Fatalf("%d release(s) handed to the client with the pending grabs unreadable", n)
	}
	if !strings.Contains(logs.String(), "not grabbing Arrival this round") {
		t.Errorf("no warning naming the title; log:\n%s", logs.String())
	}
}

func TestUnreadableBlocklistGrabsNoEpisodes(t *testing.T) {
	h := newStallHarness(t)
	logs := h.logTo()
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
	h.ix.offer("Show.S01E01.1080p.WEB-DL.x264-GRP")
	if _, err := h.c.db.Exec(`DROP TABLE blocklist`); err != nil {
		t.Fatal(err)
	}

	if _, err := h.c.GrabForScope(h.ctx, sr.ID, SeriesScope{Season: 1, Episode: 1}); err == nil {
		t.Error("episode search with an unreadable blocklist reported success")
	}
	if _, err := h.c.SearchSeriesNow(h.ctx, sr.ID); err == nil {
		t.Error("series search with an unreadable blocklist reported success")
	}
	// The grab step itself refuses too (RSS and stall fail-over reach it directly).
	full, err := h.c.series.Get(h.ctx, sr.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w, _ := wantedEpisodes(full); len(w) == 0 {
		t.Fatal("setup: the episode must be wanted, or the grab step proves nothing")
	}
	logs.Reset()
	if n, _ := h.c.grabSeriesFrom(h.ctx, full, []indexer.Release{{Title: "Show.S01E01.1080p.WEB-DL.x264-GRP", Indexer: "Fake", DownloadURL: magnetFor("Show.S01E01.1080p.WEB-DL.x264-GRP")}}); n != 0 {
		t.Errorf("grab step grabbed %d with the blocklist unreadable", n)
	}
	if n := h.adds(); n != 0 {
		t.Fatalf("%d release(s) handed to the client with the blocklist unreadable", n)
	}
	if !strings.Contains(logs.String(), "blocklist unreadable — not grabbing Show this round") {
		t.Errorf("no warning naming the show; log:\n%s", logs.String())
	}
	if _, err := h.c.RankSeriesReleases(h.ctx, sr.ID, 1, 1); err == nil {
		t.Error("ranking with an unreadable blocklist succeeded")
	}
}
