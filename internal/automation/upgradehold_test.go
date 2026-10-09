package automation

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

// holdHarness is a coordinator over a temp DB with one indexer that counts the searches
// it's asked for (and finds nothing), and a movie profile and a series profile that both
// upgrade.
type holdHarness struct {
	c          *Coordinator
	searches   *atomic.Int32
	movieRef   string
	seriesRef  string
	exec       func(q string, args ...any)
	libraryDir string
}

func newHoldHarness(t *testing.T) *holdHarness {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var searches atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("q") != "" {
			searches.Add(1)
		}
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<rss><channel></channel></rss>`)
	}))
	t.Cleanup(srv.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	ix := indexer.NewService(st.DB(), log, nil)
	if _, err := ix.Create(ctx, indexer.Indexer{Name: "All", Kind: indexer.KindTorznab, URL: srv.URL, Priority: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	q := quality.NewService(st.DB())
	h := &holdHarness{
		c: &Coordinator{
			db: st.DB(), log: log, indexers: ix, quality: q,
			movies: movies.NewService(st.DB(), nil, nil, t.TempDir(), "", nil, log),
			series: series.NewService(st.DB(), nil, t.TempDir(), log),
		},
		searches:   &searches,
		libraryDir: t.TempDir(),
		exec: func(query string, args ...any) {
			t.Helper()
			if _, err := st.DB().Exec(query, args...); err != nil {
				t.Fatal(err)
			}
		},
	}
	for _, media := range []string{quality.MediaMovie, quality.MediaSeries} {
		sp, err := q.Create(ctx, quality.StoredProfile{MediaType: media, Name: "Upgrades", UpgradesEnabled: true})
		if err != nil {
			t.Fatal(err)
		}
		ref := fmt.Sprintf("custom:%d", sp.ID)
		if media == quality.MediaMovie {
			h.movieRef = ref
		} else {
			h.seriesRef = ref
		}
	}
	return h
}

// The upgrade sweeps leave held files alone: with the only file held there's nothing to
// search for, and the indexers aren't asked. Lifting the hold brings the search back.
func TestUpgradeSweepsSkipHeldFiles(t *testing.T) {
	h := newHoldHarness(t)
	ctx := context.Background()
	h.exec(`INSERT INTO movies (id, tmdb_id, title, year, monitored, quality_profile, has_file, movie_file_path, source_release, upgrade_hold)
		VALUES (1, 1, 'Heat', 1995, 1, ?, 1, '/lib/Heat.mkv', 'Heat.1995.720p.WEB-DL.x264-GRP', 1)`, h.movieRef)
	m, err := h.c.movies.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if err := h.c.upgradeMovie(ctx, m); err != nil {
		t.Fatal(err)
	}
	if n := h.searches.Load(); n != 0 {
		t.Fatalf("held movie: %d searches, want 0", n)
	}
	h.exec(`UPDATE movies SET upgrade_hold = 0`)
	if err := h.c.upgradeMovie(ctx, m); err != nil {
		t.Fatal(err)
	}
	if n := h.searches.Load(); n != 1 {
		t.Fatalf("after Resume: %d searches, want 1", n)
	}

	h.searches.Store(0)
	h.exec(`INSERT INTO series (id, tmdb_id, title, monitored, quality_profile) VALUES (1, 7, 'Show', 1, ?)`, h.seriesRef)
	h.exec(`INSERT INTO seasons (series_id, season_number, monitored) VALUES (1, 1, 1)`)
	h.exec(`INSERT INTO episodes (series_id, season_number, episode_number, monitored, has_file, file_path, size_bytes, source_release, runtime, upgrade_hold)
		VALUES (1, 1, 1, 1, 1, '/lib/Show/S01E01.mkv', 1000000, 'Show.S01E01.720p.WEB-DL.x264-GRP', 45, 1)`)
	if err := h.c.upgradeSeries(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if n := h.searches.Load(); n != 0 {
		t.Fatalf("held episode: %d searches, want 0", n)
	}
	h.exec(`UPDATE episodes SET upgrade_hold = 0`)
	if err := h.c.upgradeSeries(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if n := h.searches.Load(); n != 1 {
		t.Fatalf("after Resume: %d searches, want 1", n)
	}
}

// The TV import gate won't replace a held episode on its own, even with a higher
// resolution; once the hold is lifted the same file is taken.
func TestWantsEpisodeFileRefusesHeld(t *testing.T) {
	h := newHoldHarness(t)
	ctx := context.Background()
	cur := filepath.Join(h.libraryDir, "Show", "S01E01.mkv")
	if err := os.MkdirAll(filepath.Dir(cur), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(cur, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	h.exec(`INSERT INTO series (id, tmdb_id, title, monitored, quality_profile) VALUES (1, 7, 'Show', 1, ?)`, h.seriesRef)
	h.exec(`INSERT INTO episodes (series_id, season_number, episode_number, monitored, has_file, file_path, size_bytes, source_release, runtime, upgrade_hold)
		VALUES (1, 1, 1, 1, 1, ?, 1000000, 'Show.S01E01.720p.WEB-DL.x264-GRP', 45, 1)`, cur)
	s, err := h.c.series.Get(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	name := "Show.S01E01.1080p.WEB-DL.x264-GRP"
	if h.c.wantsEpisodeFile(ctx, s, 1, 1, parser.Parse(name), name, 2000000) {
		t.Error("a held episode was replaced by the import gate")
	}
	h.exec(`UPDATE episodes SET upgrade_hold = 0`)
	if !h.c.wantsEpisodeFile(ctx, s, 1, 1, parser.Parse(name), name, 2000000) {
		t.Error("after Resume, a 1080p over a 720p file was refused")
	}
}
