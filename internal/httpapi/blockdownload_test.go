package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// blockServer is the real router with a real coordinator over a recording qBittorrent, and
// a job runner that records the block's slow half without running it.
func blockServer(t *testing.T) (*routeServer, *fakeQbit, *fakeJobs, *http.Cookie) {
	t.Helper()
	q := &fakeQbit{}
	srv := q.server(t)
	fj := newFakeJobs(false)
	s := newRouteServer(t, func(d *Deps) {
		dl := download.NewService(d.Store.DB(), d.Log)
		if _, err := dl.Create(context.Background(), download.Client{Name: "qb", Kind: download.KindQbittorrent, URL: srv.URL, Enabled: true}); err != nil {
			t.Fatal(err)
		}
		d.Downloads = dl
		mv := movies.NewService(d.Store.DB(), nil, nil, t.TempDir(), "", nil, d.Log)
		d.Movies = mv
		d.Automation = automation.New(mv, nil, dl, nil, d.Store.DB(), nil, d.Log, "")
		d.Jobs = fj
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	return s, q, fj, mgr
}

// Block answers with what it blocked the release for, and runs the removal and search as
// a job; a torrent tied to nothing gets a 422 pointing at Remove and is left alone.
func TestBlockDownloadNamesWhatItBlocked(t *testing.T) {
	s, q, fj, mgr := blockServer(t)
	db := s.st.DB()
	res, err := db.Exec(`INSERT INTO movies (tmdb_id, title, year, monitored) VALUES (1, 'Arrival', 2016, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO grabs (movie_id, title, media_type, info_hash) VALUES (?, 'Arrival.2016.1080p.BluRay-GRP', 'movie', ?)`, mid, testHash); err != nil {
		t.Fatal(err)
	}

	rec := s.doJSON("POST", "/api/v1/queue/"+testHash+"/block", mgr, `{"name":"Arrival.2016.1080p.BluRay-GRP"}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("block: HTTP %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		BlockedFor automation.BlockTarget `json:"blocked_for"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.BlockedFor.Kind != "movie" || body.BlockedFor.ID != mid || body.BlockedFor.Title != "Arrival (2016)" {
		t.Errorf("blocked_for = %+v", body.BlockedFor)
	}
	if specs := fj.specs(); len(specs) != 1 || specs[0].Kind != "download.block" {
		t.Errorf("jobs = %+v, want one download.block", specs)
	}

	// Nothing in the library: 422, nothing submitted, the client never asked to remove.
	other := "fedcba9876543210fedcba9876543210fedcba98"
	rec = s.doJSON("POST", "/api/v1/queue/"+other+"/block", mgr, `{"name":"Random.Thing.2019.1080p-GRP"}`)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Fatalf("unlinked block: HTTP %d: %s", rec.Code, rec.Body)
	}
	if len(fj.specs()) != 1 {
		t.Error("an unlinked block submitted a job")
	}
	paths, _ := q.snapshot()
	for _, p := range paths {
		if p == "/api/v2/torrents/delete" {
			t.Error("an unlinked torrent was removed")
		}
	}
	// The remove dialog's Block mode answers the same way.
	if rec := s.do("DELETE", "/api/v1/queue/"+other+"?mode=block&name=Random.Thing.2019.1080p-GRP", mgr); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("remove mode=block on an unlinked torrent: HTTP %d", rec.Code)
	}
}
