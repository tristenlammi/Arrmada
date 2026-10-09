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

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/mediainfo"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

// Over a library where every movie has its file, a full cycle of the periodic movie
// sweeps — search-missing, RSS, upgrades and stall detection — forks no ffprobe and
// stats no library file, and search-missing asks the indexers nothing at all. Before,
// every one of those sweeps probed every file in the library each time it ran.
func TestSearchMissingCompleteLibraryNoSearchNoProbe(t *testing.T) {
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
		// The RSS feed carries a release for one of the movies, so RSS sync has
		// something to consider.
		fmt.Fprint(w, `<rss><channel><item><title>Alpha.2001.2160p.WEB-DL.x265-GRP</title>
			<link>http://example.invalid/a.torrent</link><size>4000000000</size></item></channel></rss>`)
	}))
	t.Cleanup(srv.Close)

	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ix := indexer.NewService(st.DB(), log, "")
	if _, err := ix.Create(context.Background(), indexer.Indexer{
		Name: "Movies", Kind: indexer.KindTorznab, URL: srv.URL, Priority: 10, Enabled: true,
	}); err != nil {
		t.Fatal(err)
	}
	mv := movies.NewService(st.DB(), nil, nil, t.TempDir(), "", nil, log)
	var probes, stats atomic.Int32
	mv.SetFileAccess(
		func(string) (mediainfo.Info, error) { probes.Add(1); return mediainfo.Info{}, nil },
		func(p string) (os.FileInfo, error) { stats.Add(1); return os.Stat(p) },
	)
	c := &Coordinator{
		db: st.DB(), log: log, indexers: ix, movies: mv,
		downloads: download.NewService(st.DB(), log),
		quality:   quality.NewService(st.DB()),
	}
	ctx := context.Background()

	lib := t.TempDir()
	for i, title := range []string{"Alpha", "Bravo", "Charlie"} {
		path := filepath.Join(lib, title+" (2001)", title+".2001.1080p.BluRay.x264-GRP.mkv")
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, make([]byte, 100), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := st.DB().Exec(`INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability, has_file,
			movie_file_path, source_release, media_json)
			VALUES (?, ?, ?, 2001, 1, 'announced', 1, ?, ?, ?)`, i+1, i+1, title, path, filepath.Base(path),
			`{"path":"x","filename":"x","size_bytes":8000000000,"quality":"1080p BluRay","codec":"x264","v":2}`); err != nil {
			t.Fatal(err)
		}
		// Extra track with its file too, so the sweeps have more than the movie row to read.
		if _, err := st.DB().Exec(`INSERT INTO movie_versions (movie_id, label, monitored, has_file, file_path, size_bytes, source_release)
			VALUES (?, '4K', 1, 1, ?, 9000, 'X.2001.2160p.WEB-DL')`, i+1, path); err != nil {
			t.Fatal(err)
		}
	}
	// A grab still marked in flight, for stall detection to look at.
	if _, err := st.DB().Exec(`INSERT INTO grabs (movie_id, version_id, title, indexer, quality_profile, media_type, info_hash)
		VALUES (1, 0, 'Alpha.2001.1080p.BluRay.x264-GRP', 'Movies', '', 'movie', 'ABC')`); err != nil {
		t.Fatal(err)
	}

	c.SearchMissing(ctx)
	if n := searches.Load(); n != 0 {
		t.Errorf("search-missing searched %d times over a complete library, want 0", n)
	}
	c.RSSSync(ctx)
	c.UpgradeMovies(ctx)
	c.DetectStalled(ctx)

	if p, s := probes.Load(), stats.Load(); p != 0 || s != 0 {
		t.Fatalf("periodic sweeps probed %d files and stat'd %d, want 0 and 0", p, s)
	}
}
