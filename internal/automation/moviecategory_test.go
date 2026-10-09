package automation

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/store"
)

// categoryQbit records the category of every torrent added to it.
type categoryQbit struct {
	mu   sync.Mutex
	cats []string
}

func (f *categoryQbit) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "Ok.") })
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		f.mu.Lock()
		f.cats = append(f.cats, r.FormValue("category"))
		f.mu.Unlock()
		fmt.Fprint(w, "Ok.")
	})
	return mux
}

// A movie grabbed or uploaded through a client whose stored category is "movies" is still
// added under the configured movie category — the one the movie import sweep reads — so it
// imports.
func TestMovieGrabsUseTheConfiguredCategory(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	qb := &categoryQbit{}
	srv := httptest.NewServer(qb.handler())
	t.Cleanup(srv.Close)
	dl := download.NewService(st.DB(), log)
	if _, err := dl.Create(ctx, download.Client{Name: "qb", Kind: download.KindQbittorrent, URL: srv.URL, Category: "movies", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	ix := indexer.NewService(st.DB(), log, nil)
	if _, err := ix.Create(ctx, indexer.Indexer{Name: "Fake", Kind: indexer.KindTorznab, URL: "http://indexer.invalid", Priority: 10, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	c := &Coordinator{db: st.DB(), log: log, bus: eventbus.New(log), downloads: dl, indexers: ix, downloadsDir: t.TempDir()}

	// Unset: the default movie category, never the client's.
	if _, err := c.Grab(ctx, "Fake", "magnet:?xt=urn:btih:"+fmt.Sprintf("%040d", 1), "Film.2020.1080p"); err != nil {
		t.Fatal(err)
	}
	c.SetMovieCategory("arrmada-films")
	if _, err := c.Grab(ctx, "Fake", "magnet:?xt=urn:btih:"+fmt.Sprintf("%040d", 2), "Film.2021.1080p"); err != nil {
		t.Fatal(err)
	}
	if err := c.GrabMovieTorrent(ctx, 0, []byte("d8:announce0:e"), "film.torrent", "Film.2022.1080p"); err != nil {
		t.Fatal(err)
	}

	qb.mu.Lock()
	defer qb.mu.Unlock()
	want := []string{download.DefaultMovieCategory, "arrmada-films", "arrmada-films"}
	if fmt.Sprint(qb.cats) != fmt.Sprint(want) {
		t.Errorf("categories = %q, want %q", qb.cats, want)
	}
}
