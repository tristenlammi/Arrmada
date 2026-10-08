package automation

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/music"
	"github.com/tristenlammi/arrmada/internal/quality"
)

func TestMusicSearchWait(t *testing.T) {
	for misses, want := range map[int]time.Duration{
		-1: 0, 0: 0,
		1: 30 * time.Minute, 2: time.Hour, 6: 12 * time.Hour,
		7: 168 * time.Hour, 50: 168 * time.Hour,
	} {
		if got := musicSearchWait(misses); got != want {
			t.Errorf("musicSearchWait(%d) = %v, want %v", misses, got, want)
		}
	}
}

// An album nobody carries costs at most seven searches in its first day, then one a week.
func TestMusicBackoffBudgetPerDay(t *testing.T) {
	searches, at := 0, time.Duration(0)
	for misses := 0; at <= 24*time.Hour; misses++ {
		searches++
		at += musicSearchWait(misses + 1)
	}
	if searches > 7 {
		t.Errorf("%d searches in the first 24 h, want at most 7", searches)
	}
}

func TestAlbumReleased(t *testing.T) {
	today := "2026-10-09"
	for _, tc := range []struct {
		date string
		year int
		want bool
	}{
		{"", 0, true},
		{"", 2026, true},
		{"", 2027, false},
		{"2026-10-09", 0, true},
		{"2026-10-10", 0, false},
		{"2026-10", 0, true},
		{"2026-11", 0, false},
		{"2026", 0, true},
		{"2027", 0, false},
		{"1997-05-21", 1997, true},
	} {
		if got := albumReleased(music.Album{ReleaseDate: tc.date, Year: tc.year}, today); got != tc.want {
			t.Errorf("albumReleased(%q, %d) = %v, want %v", tc.date, tc.year, got, tc.want)
		}
	}
}

// A miss backs the album off, so a second sweep straight after makes no search for it.
func TestMusicSweepBacksOffAfterAMiss(t *testing.T) {
	h := musicTestCoord(t)
	a := h.addArtist(t, "Radiohead", "", []string{"OK Computer"}, nil, "")

	h.c.SearchMusicMissing(h.ctx)
	if len(h.queries) != 1 {
		t.Fatalf("first sweep made %d searches, want 1", len(h.queries))
	}
	al := h.album(t, a.ID, "OK Computer")
	if al.SearchMisses != 1 || al.LastSearchAt == "" {
		t.Fatalf("after a miss: misses=%d last=%q, want 1 and a stamp", al.SearchMisses, al.LastSearchAt)
	}
	h.c.SearchMusicMissing(h.ctx)
	if len(h.queries) != 1 {
		t.Errorf("second sweep searched again (%d searches total), want the album backed off", len(h.queries))
	}
}

// An album MusicBrainz has no listing for is never searched for, but still backs off so
// the listing lookup isn't repeated every sweep either.
func TestMusicSweepSkipsAlbumsWithNoListing(t *testing.T) {
	h := musicTestCoord(t)
	a := h.addArtist(t, "Radiohead", "", []string{"Untitled Next"}, map[string]bool{"Untitled Next": true}, "")

	h.c.SearchMusicMissing(h.ctx)
	if len(h.queries) != 0 {
		t.Fatalf("searched the indexers %d time(s) for an album with no listing", len(h.queries))
	}
	if al := h.album(t, a.ID, "Untitled Next"); al.SearchMisses != 1 {
		t.Errorf("misses = %d, want 1", al.SearchMisses)
	}
	h.c.SearchMusicMissing(h.ctx)
	if h.fake.listing != 1 {
		t.Errorf("MusicBrainz asked %d times for the listing, want once (backed off)", h.fake.listing)
	}
}

// Forty due albums: one sweep searches exactly 25 — never-searched first, then the ones
// searched longest ago.
func TestMusicSweepCapsAndTakesOldestFirst(t *testing.T) {
	h := musicTestCoord(t)
	titles := make([]string, 40)
	for i := range titles {
		titles[i] = fmt.Sprintf("Album %02d", i)
	}
	a := h.addArtist(t, "Prolific", "", titles, nil, "")
	// Albums 05..39 were searched once, (i) hours ago, so all are past the 30-minute
	// wait and the oldest is Album 39. Albums 00..04 have never been searched.
	for i := 5; i < 40; i++ {
		if _, err := h.st.DB().Exec(
			`UPDATE albums SET search_misses = 1, last_search_at = datetime('now', ?) WHERE artist_id = ? AND title = ?`,
			fmt.Sprintf("-%d hours", i), a.ID, titles[i]); err != nil {
			t.Fatal(err)
		}
	}

	h.c.SearchMusicMissing(h.ctx)
	if len(h.queries) != musicSweepCap {
		t.Fatalf("one sweep searched %d albums, want %d", len(h.queries), musicSweepCap)
	}
	searched := map[string]bool{}
	for _, q := range h.queries {
		searched[strings.TrimPrefix(q, "Prolific ")] = true
	}
	// The five never searched, then the twenty oldest: 39 down to 20.
	for i := 0; i < 5; i++ {
		if !searched[titles[i]] {
			t.Errorf("never-searched %s was left for later", titles[i])
		}
	}
	for i := 20; i < 40; i++ {
		if !searched[titles[i]] {
			t.Errorf("%s (searched %d h ago) was left for later", titles[i], i)
		}
	}
	if searched[titles[19]] {
		t.Error("Album 19 was searched ahead of older ones")
	}
}

// An album that isn't out yet is neither searched nor counted as a miss.
func TestMusicSweepSkipsUnreleasedAlbums(t *testing.T) {
	h := musicTestCoord(t)
	next := time.Now().AddDate(0, 1, 0).Format("2006-01-02")
	a := h.addArtist(t, "Radiohead", "", []string{"Next Year"}, nil, next)
	b := h.addArtist(t, "Muse", "", []string{"Far Off"}, nil, "2999")

	h.c.SearchMusicMissing(h.ctx)
	if len(h.queries) != 0 || h.fake.listing != 0 {
		t.Fatalf("future albums: %d searches, %d listing lookups; want none", len(h.queries), h.fake.listing)
	}
	if al := h.album(t, a.ID, "Next Year"); al.SearchMisses != 0 || al.LastSearchAt != "" {
		t.Errorf("future-dated album was marked: misses=%d last=%q", al.SearchMisses, al.LastSearchAt)
	}
	if al := h.album(t, b.ID, "Far Off"); al.SearchMisses != 0 {
		t.Errorf("year-only future album misses = %d, want 0", al.SearchMisses)
	}
}

// A failed search says nothing about the album, so it's not held against it — whether the
// search itself errored or every indexer did.
func TestMusicSweepSearchErrorIsNotAMiss(t *testing.T) {
	for name, answer := range map[string]func(indexer.SearchQuery) (indexer.SearchResult, error){
		"search error": func(indexer.SearchQuery) (indexer.SearchResult, error) {
			return indexer.SearchResult{}, errors.New("db locked")
		},
		"every indexer failed": func(indexer.SearchQuery) (indexer.SearchResult, error) {
			return indexer.SearchResult{Errors: map[string]string{"Test": "timeout"}}, nil
		},
	} {
		t.Run(name, func(t *testing.T) {
			h := musicTestCoord(t)
			h.releases = answer
			a := h.addArtist(t, "Radiohead", "", []string{"OK Computer"}, nil, "")
			h.c.SearchMusicMissing(h.ctx)
			if len(h.queries) != 1 {
				t.Fatalf("%d searches, want 1", len(h.queries))
			}
			if al := h.album(t, a.ID, "OK Computer"); al.SearchMisses != 0 || al.LastSearchAt != "" {
				t.Errorf("misses=%d last=%q, want the album untouched", al.SearchMisses, al.LastSearchAt)
			}
		})
	}
}

// Out of disk space for one release means out for all of them: the sweep stops there.
func TestMusicSweepStopsWhenOutOfSpace(t *testing.T) {
	h := musicTestCoord(t)
	h.releases = func(q indexer.SearchQuery) (indexer.SearchResult, error) {
		r := rel(strings.Replace(q.Text, "Radiohead ", "Radiohead - ", 1) + " [FLAC]")
		r.SizeBytes = 1 << 62 // no disk is this big
		return indexer.SearchResult{Releases: []indexer.Release{r}}, nil
	}
	a := h.addArtist(t, "Radiohead", "", []string{"OK Computer", "Kid A", "Amnesiac"}, nil, "")

	h.c.SearchMusicMissing(h.ctx)
	if len(h.queries) != 1 {
		t.Errorf("%d searches after running out of space, want the sweep to stop at 1", len(h.queries))
	}
	if len(h.grabs) != 0 {
		t.Errorf("grabbed %v with no space for it", h.grabs)
	}
	for _, title := range []string{"OK Computer", "Kid A", "Amnesiac"} {
		if al := h.album(t, a.ID, title); al.SearchMisses != 0 {
			t.Errorf("%s: no space was counted as a miss", title)
		}
	}
}

// A grab clears the album's backoff in the DB.
func TestMusicSweepGrabResetsMisses(t *testing.T) {
	h := musicTestCoord(t)
	h.releases = found(rel("Radiohead - OK Computer (1997) [FLAC]"))
	a := h.addArtist(t, "Radiohead", "", []string{"OK Computer"}, nil, "")
	if _, err := h.st.DB().Exec(
		`UPDATE albums SET search_misses = 3, last_search_at = datetime('now', '-3 hours') WHERE artist_id = ?`, a.ID); err != nil {
		t.Fatal(err)
	}

	h.c.SearchMusicMissing(h.ctx)
	if len(h.grabs) != 1 {
		t.Fatalf("grabs = %v, want the FLAC", h.grabs)
	}
	if al := h.album(t, a.ID, "OK Computer"); al.SearchMisses != 0 || al.LastSearchAt == "" {
		t.Errorf("after a grab: misses=%d last=%q, want 0 and a stamp", al.SearchMisses, al.LastSearchAt)
	}
}

// grabAlbum names what happened, which is what the sweep's backoff keys on.
func TestGrabAlbumOutcomes(t *testing.T) {
	h := musicTestCoord(t)
	lossless, err := h.c.quality.Create(h.ctx, quality.StoredProfile{
		MediaType: quality.MediaMusic, Name: "Lossless only",
		FormatScores:   map[string]int{"FLAC-24": 130, "FLAC": 120, "ALAC": 110, "WAV": 100},
		MinFormatScore: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	a := h.addArtist(t, "Radiohead", fmt.Sprintf("custom:%d", lossless.ID), []string{"OK Computer"}, nil, "")
	al := h.album(t, a.ID, "OK Computer")

	for _, tc := range []struct {
		name     string
		releases []indexer.Release
		want     string
		miss     bool
	}{
		{"nothing at all", nil, outcomeNoResults, true},
		{"other albums only", []indexer.Release{rel("Radiohead - Kid A (2000) [FLAC]"), rel("Muse - Origin of Symmetry [FLAC]")}, outcomeNoMatch, true},
		{"MP3 against lossless only", []indexer.Release{rel("Radiohead - OK Computer (1997) [MP3 320]")}, outcomeBelowProfile, true},
		{"a FLAC", []indexer.Release{rel("Radiohead - OK Computer (1997) [FLAC]")}, outcomeGrabbed, false},
	} {
		h.releases = found(tc.releases...)
		out := h.c.grabAlbum(h.ctx, a, al)
		if out.Code != tc.want || out.miss() != tc.miss {
			t.Errorf("%s: outcome %+v, want %s (miss=%v)", tc.name, out, tc.want, tc.miss)
		}
	}
	if len(h.grabs) != 1 || !strings.Contains(h.grabs[0], "[FLAC]") {
		t.Errorf("grabs = %v, want just the FLAC", h.grabs)
	}
	// The FLAC is now an in-flight grab, so finding it again is "already grabbed".
	h.releases = found(rel("Radiohead - OK Computer (1997) [FLAC]"))
	if out := h.c.grabAlbum(h.ctx, a, al); out.Code != outcomeBlocked || !out.miss() {
		t.Errorf("re-finding the pending grab: %+v, want blocked", out)
	}
}
