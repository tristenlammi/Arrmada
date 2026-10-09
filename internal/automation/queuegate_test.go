package automation

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/store"
)

// With the download client down, the search sweeps sit the cycle out before asking any
// indexer anything. The same library with a working (here: no) client does search, which
// proves the fixture would have.
func TestSweepsSkipWhileClientDown(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		w.Header().Set("Content-Type", "application/xml")
		fmt.Fprint(w, `<rss><channel><item><title>Alpha.2001.1080p.WEB-DL.x264-GRP</title>
			<link>http://example.invalid/a.torrent</link><size>4000000000</size></item>
			<item><title>Show.S01E01.1080p.WEB-DL.x264-GRP</title>
			<link>http://example.invalid/b.torrent</link><size>1000000000</size></item></channel></rss>`)
	}))
	t.Cleanup(srv.Close)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	ix := indexer.NewService(st.DB(), log, nil)
	if _, err := ix.Create(ctx, indexer.Indexer{Name: "All", Kind: indexer.KindTorznab, URL: srv.URL, Priority: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	for _, q := range []string{
		`INSERT INTO movies (tmdb_id, title, year, monitored, min_availability) VALUES (1, 'Alpha', 2001, 1, 'announced')`,
		`INSERT INTO series (id, tmdb_id, title, monitored) VALUES (1, 7, 'Show', 1)`,
		`INSERT INTO episodes (series_id, season_number, episode_number, air_date, monitored) VALUES (1, 1, 1, '2020-01-01', 1)`,
	} {
		if _, err := st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	dl := download.NewService(st.DB(), log)
	c := &Coordinator{
		db: st.DB(), log: log, indexers: ix, downloads: dl,
		movies:  movies.NewService(st.DB(), nil, nil, t.TempDir(), "", nil, log),
		series:  series.NewService(st.DB(), nil, t.TempDir(), log),
		quality: quality.NewService(st.DB()),
	}
	sweeps := map[string]func(context.Context){
		"SearchMissing":       c.SearchMissing,
		"RSSSync":             c.RSSSync,
		"SearchSeriesMissing": c.SearchSeriesMissing,
		"RSSSyncSeries":       c.RSSSyncSeries,
	}

	// Control: no client configured is an empty, complete queue — every sweep asks.
	for name, run := range sweeps {
		c.indexers = indexer.NewService(st.DB(), log, nil) // the RSS feed is cached per service
		before := hits.Load()
		run(ctx)
		if hits.Load() == before {
			t.Fatalf("control: %s asked no indexer — the fixture can't show the skip", name)
		}
	}

	// A client that doesn't answer.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	if _, err := dl.Create(ctx, download.Client{Name: "qb", Kind: download.KindQbittorrent, URL: deadURL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	// Reset the movie's backoff so the control's miss can't explain a skip.
	if _, err := st.DB().Exec(`UPDATE movies SET search_misses = 0, last_search_at = ''`); err != nil {
		t.Fatal(err)
	}
	for name, run := range sweeps {
		c.indexers = indexer.NewService(st.DB(), log, nil)
		before := hits.Load()
		run(ctx)
		if n := hits.Load() - before; n != 0 {
			t.Errorf("%s made %d indexer requests with the download client down, want 0", name, n)
		}
	}
}
