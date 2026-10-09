package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/store"
)

// A grabbed movie whose torrent arrives under a name that parses to nothing like it is
// still Arrmada's: the Downloads feed labels it by hash (with its seed rule), keeps the
// film out of Searching, and the movie grid shows its progress. A torrent no grab knows
// is "not managed", even when its name matches a library title.
func TestDownloadsFeedJoinsByHash(t *testing.T) {
	const hash = "ABCDEF0123456789ABCDEF0123456789ABCDEF01"
	const stranger = "0000000000000000000000000000000000000001"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v2/auth/login":
			fmt.Fprint(w, "Ok.")
		case "/api/v2/torrents/info":
			fmt.Fprintf(w, `[{"hash":%q,"name":"PH.2002.x265-Goki","state":"downloading","progress":0.4,"size":100,"amount_left":60},
				{"hash":%q,"name":"Pokemon.Heroes.2002.1080p.BluRay.x264-OTHER","state":"downloading","progress":0.1,"size":100,"amount_left":90}]`, hash, stranger)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctx := context.Background()
	dl := download.NewService(st.DB(), log)
	if _, err := dl.Create(ctx, download.Client{Name: "qb", Kind: download.KindQbittorrent, URL: srv.URL, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	mv := movies.NewService(st.DB(), nil, nil, t.TempDir(), "", nil, log)
	a := &api{deps: Deps{
		Store: st, Log: log, Downloads: dl, Movies: mv, Quality: quality.NewService(st.DB()),
		Automation: automation.New(mv, nil, dl, nil, st.DB(), nil, log, ""),
		Config:     config.Config{DownloadsDir: t.TempDir()},
	}}
	if _, err := st.DB().Exec(`INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability) VALUES (1, 1, 'Pokémon Heroes', 2002, 1, 'announced')`); err != nil {
		t.Fatal(err)
	}
	if _, err := st.DB().Exec(`INSERT INTO grabs (movie_id, version_id, title, indexer, media_type, info_hash, seed_enabled, seed_ratio)
		VALUES (1, 0, 'Pokemon Heroes (2002) 1080p BDRip x265 10bit AC3 5 1 DUAL - Goki', 'x', 'movie', lower(?), 1, 2)`, hash); err != nil {
		t.Fatal(err)
	}

	rec := httptest.NewRecorder()
	a.handleDownloadsFeed(rec, httptest.NewRequest(http.MethodGet, "/api/v1/downloads", nil))
	var feed struct {
		Downloads []struct {
			Hash      string `json:"hash"`
			MediaType string `json:"media_type"`
			SeedKnown bool   `json:"seed_known"`
		} `json:"downloads"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &feed); err != nil {
		t.Fatal(err)
	}
	if w := a.buildWanted(ctx, wantedKinds{automation.AttemptMovie: true}); len(w.Searching) != 0 {
		t.Errorf("a movie downloading under another name is listed as searching: %+v", w.Searching)
	}
	if len(feed.Downloads) != 2 {
		t.Fatalf("downloads = %+v", feed.Downloads)
	}
	for _, d := range feed.Downloads {
		switch d.Hash {
		case hash:
			if !d.SeedKnown || d.MediaType != "movie" {
				t.Errorf("Arrmada's own torrent: %+v, want its seed rule by hash", d)
			}
		case stranger:
			if d.SeedKnown {
				t.Errorf("a torrent no grab knows borrowed a seed rule by name: %+v", d)
			}
		}
	}

	// The grid's join (handleListMovies): the movie's acquisitions, then its torrent by hash.
	acqs, err := a.deps.Automation.ActiveByItem(ctx, "movie")
	if err != nil {
		t.Fatal(err)
	}
	queue, _ := dl.Queue(ctx)
	m, _ := mv.Get(ctx, 1)
	if d := movieDownload(m, acqs[1], queueByHash(queue), queue); d == nil || d.Progress != 0.4 {
		t.Errorf("grid progress: %+v, want the grabbed torrent's 0.4", d)
	}
}
