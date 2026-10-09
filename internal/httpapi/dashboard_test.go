package httpapi

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

func dashAPI(t *testing.T) *api {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &api{deps: Deps{
		Store:  st,
		Log:    slog.New(slog.NewTextHandler(io.Discard, nil)),
		Config: config.Config{DataDir: t.TempDir()},
	}}
}

// The counts drive four tiles on a page that loads on every visit, so a column
// renamed out from under them must fail here rather than silently render zeros.
func TestLibraryCountsQueryTheRealSchema(t *testing.T) {
	a := dashAPI(t)
	db := a.deps.Store.DB()
	ctx := context.Background()

	for _, q := range []string{
		`INSERT INTO movies (tmdb_id, title, monitored, has_file) VALUES (1, 'Have', 1, 1)`,
		`INSERT INTO movies (tmdb_id, title, monitored, has_file) VALUES (2, 'Want', 1, 0)`,
		`INSERT INTO movies (tmdb_id, title, monitored, has_file) VALUES (3, 'Ignored', 0, 0)`,
	} {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	lc := a.libraryCounts(ctx)
	if lc.Movies != 3 {
		t.Errorf("movies = %d, want 3", lc.Movies)
	}
	// Unmonitored titles aren't missing — nobody asked for them.
	if lc.MoviesMissing != 1 {
		t.Errorf("movies missing = %d, want 1 (the unmonitored one doesn't count)", lc.MoviesMissing)
	}
}

// The activity feed is a three-way UNION with joins; a typo in it would show as an
// empty panel rather than an error, so assert it actually returns rows.
func TestRecentActivityUnionsEveryModule(t *testing.T) {
	a := dashAPI(t)
	db := a.deps.Store.DB()
	ctx := context.Background()

	seed := []string{
		`INSERT INTO movies (id, tmdb_id, title) VALUES (1, 10, 'Dune')`,
		`INSERT INTO movie_events (movie_id, event, detail) VALUES (1, 'grabbed', '2160p')`,
		`INSERT INTO series (id, tmdb_id, title) VALUES (1, 20, 'Silo')`,
		`INSERT INTO series_events (series_id, event, detail) VALUES (1, 'imported', 'S02E04')`,
		`INSERT INTO books (id, ol_key, title) VALUES (1, 'OL1W', 'Red Rising')`,
		`INSERT INTO book_events (book_id, event, detail) VALUES (1, 'imported', 'M4B')`,
	}
	for _, q := range seed {
		if _, err := db.ExecContext(ctx, q); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}

	got := a.recentActivity(ctx)
	if len(got) != 3 {
		t.Fatalf("got %d events, want one from each module: %+v", len(got), got)
	}
	kinds := map[string]string{}
	for _, e := range got {
		kinds[e.Kind] = e.Title
		if e.AtMS == 0 {
			t.Errorf("event %+v has no timestamp", e)
		}
	}
	for kind, title := range map[string]string{"movie": "Dune", "series": "Silo", "book": "Red Rising"} {
		if kinds[kind] != title {
			t.Errorf("%s event resolved to %q, want %q — the join is wrong", kind, kinds[kind], title)
		}
	}
}

// Movies, TV and books normally live on one array. Reporting them as separate
// volumes would print the same bar five times and imply five times the space.
func TestStorageVolumesFoldSharedFilesystems(t *testing.T) {
	dir := t.TempDir()
	a := &api{deps: Deps{Config: config.Config{
		MoviesDir: dir, TVDir: dir + "/tv", EbooksDir: dir + "/books",
	}}}
	for _, sub := range []string{"/tv", "/books"} {
		if err := os.MkdirAll(dir+sub, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	vols := a.storageVolumes()
	if len(vols) == 0 {
		t.Skip("this platform can't measure disk usage")
	}
	if len(vols) != 1 {
		t.Fatalf("got %d volumes for one filesystem: %+v", len(vols), vols)
	}
	if len(vols[0].Roots) != 3 {
		t.Errorf("roots = %v, want all three folded onto the one volume", vols[0].Roots)
	}
	if vols[0].UsedPct <= 0 || vols[0].UsedPct > 100 {
		t.Errorf("used_pct = %v, outside 0-100", vols[0].UsedPct)
	}
}

// A torrent nobody is seeding used to count as downloading, so a queue of dead torrents
// looked busy on the Dashboard. It counts as stalled now, and a queued seed stays a seed.
func TestSummarizeQueueSeparatesStalled(t *testing.T) {
	q := summarizeQueue([]download.Item{
		{RawState: "stalledDL", State: "downloading", Progress: 0.1, RemainingBytes: 1},
		{RawState: "downloading", State: "downloading", Progress: 0.5, RemainingBytes: 1, DownSpeed: 100},
		{RawState: "metaDL", State: "downloading"},
		{RawState: "queuedDL", State: "downloading", Progress: 0.2, RemainingBytes: 1},
		{RawState: "queuedUP", State: "seeding", Progress: 1},
		{RawState: "stalledUP", State: "seeding", Progress: 1, UpSpeed: 7},
		{RawState: "stoppedDL", State: "paused", Progress: 0.3, RemainingBytes: 1},
		{RawState: "missingFiles", State: "error"},
	})
	want := queueSummary{Downloading: 3, Stalled: 1, Seeding: 2, Paused: 1, Errored: 1, DownSpeed: 100, UpSpeed: 7}
	if q != want {
		t.Errorf("summary = %+v\nwant      %+v", q, want)
	}
}

// A download client that can't be listed is an outage, not an empty queue: the payload
// carries queue_note and zero counts, so the tile can say "Client unreachable" instead of
// "0 seeding".
func TestDashboardQueueNoteWhenClientUnreachable(t *testing.T) {
	a := dashAPI(t)
	dl := download.NewService(a.deps.Store.DB(), a.deps.Log)
	// Port 1 on loopback refuses the connection at once; nothing is ever contacted.
	if _, err := dl.Create(context.Background(), download.Client{Name: "qb", Kind: download.KindQbittorrent, URL: "http://127.0.0.1:1", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	a.deps.Downloads = dl

	rec := httptest.NewRecorder()
	a.handleDashboard(rec, httptest.NewRequest("GET", "/api/v1/dashboard", nil))
	var body struct {
		Queue     queueSummary `json:"queue"`
		QueueNote string       `json:"queue_note"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	if body.QueueNote == "" {
		t.Fatal("an unreachable client left queue_note empty")
	}
	if body.Queue != (queueSummary{}) {
		t.Errorf("queue = %+v, want zero counts alongside the note", body.Queue)
	}
}

// dashPlex runs the dashboard with an Insights service over the test store's settings and
// returns the Plex half of the payload.
func dashPlex(t *testing.T, url, token string) (configured bool, note string) {
	t.Helper()
	a := dashAPI(t)
	ctx := context.Background()
	ins := insights.NewService(a.deps.Store.DB(), settings.NewService(a.deps.Store.DB()), nil, nil, a.deps.Log)
	if url != "" || token != "" {
		if err := ins.SetConfig(ctx, url, &token, nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	a.deps.Insights = ins

	rec := httptest.NewRecorder()
	a.handleDashboard(rec, httptest.NewRequest("GET", "/api/v1/dashboard", nil))
	var body struct {
		PlexConfigured bool   `json:"plex_configured"`
		StreamsNote    string `json:"streams_note"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	return body.PlexConfigured, body.StreamsNote
}

// With no Plex server set up there is nothing to be unreachable: the page should say
// "not connected yet", not print "plex is not configured" as an outage.
func TestDashboardPlexNotConfigured(t *testing.T) {
	configured, note := dashPlex(t, "", "")
	if configured {
		t.Error("plex_configured = true with no URL or token")
	}
	if note != "" {
		t.Errorf("streams_note = %q, want empty when Plex isn't set up", note)
	}
}

// A configured server that can't be reached is still reported, with the real error.
func TestDashboardPlexUnreachable(t *testing.T) {
	// Port 1 on loopback refuses the connection at once; nothing is ever contacted.
	configured, note := dashPlex(t, "http://127.0.0.1:1", "test-token")
	if !configured {
		t.Error("plex_configured = false with a URL and token set")
	}
	if note == "" {
		t.Error("an unreachable Plex left streams_note empty")
	}
}
