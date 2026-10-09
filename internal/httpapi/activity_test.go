package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/store"
)

// The Downloads feed says whether there is a download client and whether it's answering,
// and never reports free space it couldn't measure.
func TestDownloadsFeedClientState(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	dl := download.NewService(st.DB(), log)
	a := &api{deps: Deps{
		Store: st, Log: log, Downloads: dl,
		Movies: movies.NewService(st.DB(), nil, nil, t.TempDir(), "", nil, log),
		Config: config.Config{DownloadsDir: filepath.Join(t.TempDir(), "does-not-exist")},
	}}
	// A wanted movie, so the Searching list has something to label.
	if _, err := st.DB().Exec(`INSERT INTO movies (tmdb_id, title, year, monitored, min_availability) VALUES (1, 'Alpha', 2001, 1, 'announced')`); err != nil {
		t.Fatal(err)
	}

	type feed struct {
		FreeGB    *float64 `json:"free_gb"`
		DiskPath  string   `json:"disk_path"`
		Searching []struct {
			State string `json:"state"`
		} `json:"searching"`
		Clients *struct {
			Configured int    `json:"configured"`
			Enabled    int    `json:"enabled"`
			OK         bool   `json:"ok"`
			Name       string `json:"name"`
			Error      string `json:"error"`
			Since      string `json:"since"`
		} `json:"clients"`
	}
	get := func() feed {
		t.Helper()
		dl.InvalidateSnapshot()
		rec := httptest.NewRecorder()
		a.handleDownloadsFeed(rec, httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status %d: %s", rec.Code, rec.Body)
		}
		var f feed
		if err := json.Unmarshal(rec.Body.Bytes(), &f); err != nil {
			t.Fatal(err)
		}
		var raw map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &raw)
		if v, ok := raw["free_gb"]; !ok || v != nil {
			t.Errorf("free_gb for a missing folder = %v (present %v), want null", v, ok)
		}
		return f
	}

	// No client at all: nothing is down, there's just nothing to download with.
	f := get()
	if f.Clients == nil || f.Clients.Configured != 0 || !f.Clients.OK {
		t.Fatalf("no clients: %+v", f.Clients)
	}
	if f.DiskPath == "" || len(f.Searching) != 1 || f.Searching[0].State != "" {
		t.Errorf("no clients: disk_path %q, searching %+v", f.DiskPath, f.Searching)
	}

	// A client pointed at a dead address: not answering, with its error and since when.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	c, err := dl.Create(ctx, download.Client{Name: "qBittorrent", Kind: download.KindQbittorrent, URL: deadURL, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	f = get()
	if f.Clients == nil || f.Clients.Configured != 1 || f.Clients.Enabled != 1 || f.Clients.OK {
		t.Fatalf("dead client: %+v", f.Clients)
	}
	if f.Clients.Name != "qBittorrent" || f.Clients.Error == "" || f.Clients.Since == "" {
		t.Errorf("dead client should be named, with its error and outage start: %+v", f.Clients)
	}
	if len(f.Searching) != 1 || f.Searching[0].State != "unknown" {
		t.Errorf("with the queue unknown, wanted titles must read unknown: %+v", f.Searching)
	}

	// Pointed at a working client: healthy, and the page looks as it always did.
	up := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			fmt.Fprint(w, "Ok.")
		case "/api/v2/torrents/info":
			fmt.Fprint(w, "[]")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(up.Close)
	c.URL = up.URL
	if _, err := dl.Update(ctx, c); err != nil {
		t.Fatal(err)
	}
	f = get()
	if f.Clients == nil || !f.Clients.OK || f.Clients.Error != "" || f.Clients.Since != "" {
		t.Fatalf("working client: %+v", f.Clients)
	}
	if f.Searching[0].State != "" {
		t.Errorf("with a working client, wanted titles are plain searching: %+v", f.Searching)
	}
}

// ACQ-17: a torrent no grab knows is labelled by its download category, so a music
// torrent is Music — "Inception" the soundtrack never counts as the film. One Arrmada
// grabbed takes its type and profile from the acquisition record (ACQ-25) instead.
func TestQueueMediaTypeByCategory(t *testing.T) {
	cases := map[string]string{
		download.CategoryMusic:        "music",
		download.CategoryTV:           "series",
		download.CategoryBooks:        "book",
		download.DefaultMovieCategory: "movie",
		"movies-custom":               "movie",
		"":                            "movie",
	}
	for cat, want := range cases {
		if got := queueMediaType(cat); got != want {
			t.Errorf("queueMediaType(%q) = %s, want %s", cat, got, want)
		}
	}
}
