package automation

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/store"
)

// savePathQbit records the save path of every torrent added to it.
type savePathQbit struct {
	mu    sync.Mutex
	paths []string
}

func (f *savePathQbit) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v2/auth/login", func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "Ok.") })
	mux.HandleFunc("/api/v2/torrents/add", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseMultipartForm(1 << 20)
		f.mu.Lock()
		f.paths = append(f.paths, r.FormValue("savepath"))
		f.mu.Unlock()
		fmt.Fprint(w, "Ok.")
	})
	return mux
}

// Changing the Downloads folder in Settings reaches the very next grab's save path, with
// no restart: the coordinator reads the folder on every add.
func TestCoordinatorSavePathLive(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()

	qb := &savePathQbit{}
	srv := httptest.NewServer(qb.handler())
	t.Cleanup(srv.Close)
	dl := download.NewService(st.DB(), log)
	if _, err := dl.Create(ctx, download.Client{Name: "qb", Kind: download.KindQbittorrent, URL: srv.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}

	c := &Coordinator{db: st.DB(), log: log, bus: eventbus.New(log), downloads: dl, downloadsDir: "/startup/downloads"}
	var current atomic.Value
	current.Store("/storage/torrents")
	c.SetDownloadsDirFunc(func() string { return current.Load().(string) })

	if _, err := c.addTorrentFile(ctx, []byte("d8:announce0:e"), "a.torrent", "First", ""); err != nil {
		t.Fatal(err)
	}
	current.Store("/storage/new-torrents") // saved in Settings → Library
	if _, err := c.addTorrentFile(ctx, []byte("d8:announce0:e"), "b.torrent", "Second", ""); err != nil {
		t.Fatal(err)
	}
	qb.mu.Lock()
	defer qb.mu.Unlock()
	if len(qb.paths) != 2 || qb.paths[0] != "/storage/torrents" || qb.paths[1] != "/storage/new-torrents" {
		t.Fatalf("save paths = %q, want the folder in use at each grab", qb.paths)
	}
	if c.downloadsPath() == c.downloadsDir {
		t.Error("the startup folder must not win over the live one")
	}
}
