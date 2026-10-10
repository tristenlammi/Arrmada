package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

// recentAPI is an api over a scratch library, with a fixed (empty) enrichment snapshot.
func recentAPI(t *testing.T, seed ...string) *api {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	for _, q := range seed {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatalf("%s: %v", q, err)
		}
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	a := &api{deps: Deps{
		Log:    log,
		Movies: movies.NewService(st.DB(), nil, nil, t.TempDir(), "", nil, log),
		Series: series.NewService(st.DB(), nil, t.TempDir(), log),
	}}
	enrichSnaps.Store(a, &enrichSnapEntry{at: time.Now().Add(time.Hour), snap: emptySnap()})
	t.Cleanup(func() { enrichSnaps.Delete(a); recentCache.Delete(a) })
	return a
}

func fetchRecent(t *testing.T, a *api) []discoverCard {
	t.Helper()
	rec := httptest.NewRecorder()
	a.handleDiscoverRecentlyAdded(rec, httptest.NewRequest("GET", "/api/v1/discover/recently-added", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Items []discoverCard `json:"items"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	return body.Items
}

func recentKeys(cards []discoverCard) []string {
	var out []string
	for _, c := range cards {
		out = append(out, c.MediaType+":"+strconv.Itoa(c.TMDBID))
	}
	return out
}

// The row comes from the import history: one card per show however many episodes
// landed, newest first, only the last 30 days, and only titles that still have a file.
func TestRecentlyAddedFromEvents(t *testing.T) {
	a := recentAPI(t,
		`INSERT INTO movies (id, tmdb_id, title, year, poster_url, has_file) VALUES
			(1, 101, 'Harbour Lights', 2024, 'p1', 1),
			(2, 102, 'Old Arrival', 2010, 'p2', 1),
			(3, 103, 'Gone Again', 2020, 'p3', 0),
			(4, 104, 'Only Added', 2021, 'p4', 1)`,
		`INSERT INTO movie_events (movie_id, event, created_at) VALUES
			(1, 'imported', datetime('now', '-2 days')),
			(2, 'imported', datetime('now', '-40 days')),
			(3, 'imported', datetime('now', '-1 days')),
			(4, 'added', datetime('now', '-1 days'))`,
		`INSERT INTO series (id, tmdb_id, title, year, poster_url) VALUES (1, 201, 'Saltwind', 2023, 'ps')`,
		`INSERT INTO episodes (series_id, season_number, episode_number, has_file) VALUES (1, 1, 1, 1), (1, 1, 2, 1)`,
		`INSERT INTO series_events (series_id, event, created_at) VALUES
			(1, 'imported', datetime('now', '-5 days')),
			(1, 'imported', datetime('now', '-1 hours'))`,
	)
	got := recentKeys(fetchRecent(t, a))
	want := []string{"series:201", "movie:101"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("row = %v, want %v", got, want)
	}
}

// No adult title reaches the row, from the library or from Plex.
func TestRecentlyAddedAdultFilter(t *testing.T) {
	a := recentAPI(t,
		`INSERT INTO movies (id, tmdb_id, title, year, poster_url, has_file) VALUES
			(1, 101, 'Brazzers Night Shift', 2024, 'p1', 1),
			(2, 102, 'Harbour Lights', 2024, 'p2', 1)`,
		`INSERT INTO movie_events (movie_id, event, created_at) VALUES
			(1, 'imported', datetime('now', '-1 days')),
			(2, 'imported', datetime('now', '-2 days'))`,
	)
	if got := recentKeys(fetchRecent(t, a)); len(got) != 1 || got[0] != "movie:102" {
		t.Fatalf("library row = %v, want only movie:102", got)
	}

	// From Plex: a lookup that answers an adult-titled card is dropped, a clean one kept.
	now := time.Now().Unix()
	lookup := func(media string, tmdb int) (metadata.DiscoverItem, bool) {
		title := "Quiet Evening"
		if tmdb == 7 {
			title = "Vixen After Hours"
		}
		return metadata.DiscoverItem{MediaType: media, TMDBID: tmdb, Title: title, PosterURL: "p"}, true
	}
	merged := mergeRecent(nil, []insights.RecentTMDB{{Media: "movie", TMDB: 7, AddedAt: now}, {Media: "movie", TMDB: 8, AddedAt: now - 10}}, lookup, now-3600)
	if len(merged) != 1 || merged[0].TMDBID != 8 {
		t.Fatalf("merged = %+v, want only 8", merged)
	}
}

// Plex titles join the library's: a title both know is one card at its newest arrival,
// one Plex added outside Arrmada is looked up, and one older than the window is left out.
func TestRecentlyAddedMergesPlex(t *testing.T) {
	now := time.Now().Unix()
	local := []recentTitle{
		{at: now - 300, item: metadata.DiscoverItem{MediaType: "movie", TMDBID: 1, Title: "Harbour Lights", PosterURL: "p"}},
		{at: now - 600, item: metadata.DiscoverItem{MediaType: "series", TMDBID: 2, Title: "Saltwind", PosterURL: "p"}},
	}
	looked := 0
	lookup := func(media string, tmdb int) (metadata.DiscoverItem, bool) {
		looked++
		return metadata.DiscoverItem{MediaType: media, TMDBID: tmdb, Title: "Outside Title", PosterURL: "p"}, true
	}
	plexItems := []insights.RecentTMDB{
		{Media: "series", TMDB: 2, AddedAt: now - 10},      // Plex saw it later: moves it up
		{Media: "movie", TMDB: 3, AddedAt: now - 100},      // added outside Arrmada
		{Media: "movie", TMDB: 4, AddedAt: now - 90*86400}, // too old
	}
	got := mergeRecent(local, plexItems, lookup, now-30*86400)
	var keys []string
	for _, it := range got {
		keys = append(keys, it.MediaType+":"+strconv.Itoa(it.TMDBID))
	}
	want := []string{"series:2", "movie:3", "movie:1"}
	if len(keys) != 3 || keys[0] != want[0] || keys[1] != want[1] || keys[2] != want[2] {
		t.Fatalf("merged = %v, want %v", keys, want)
	}
	if looked != 1 {
		t.Errorf("looked up %d titles, want 1 (only the one Arrmada doesn't have)", looked)
	}
}
