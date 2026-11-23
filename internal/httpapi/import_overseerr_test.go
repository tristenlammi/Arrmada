package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/series"
)

// fakeSeerr answers Overseerr/Jellyseerr's request list with one page of results, and any
// movie's details with a title.
func fakeSeerr(t *testing.T, results string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/api/v1/request" && r.URL.Query().Get("skip") == "0":
			_, _ = w.Write([]byte(`{"results": [` + results + `]}`))
		case r.URL.Path == "/api/v1/request":
			_, _ = w.Write([]byte(`{"results": []}`))
		case strings.HasPrefix(r.URL.Path, "/api/v1/movie/"):
			_, _ = w.Write([]byte(`{"title": "A film", "releaseDate": "2001-01-01"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// Issue #130: a Seerr history full of requests whose media was since deleted came in as
// approved requests, and every one of them was searched. Now only what is still wanted
// comes in to be searched (and a few at a time); deleted, declined and finished history
// that the library doesn't have is left out, and finished history it does have shows as
// a ready request that searches nothing.
func TestImportOverseerrLeavesOutRemovedAndFinished(t *testing.T) {
	root := t.TempDir()
	var runner *jobs.Runner
	var mv *movies.Service
	s := newRouteServer(t, func(d *Deps) {
		db := d.Store.DB()
		var err error
		if runner, err = jobs.New(context.Background(), db, d.Log, nil); err != nil {
			t.Fatal(err)
		}
		d.Jobs = runner
		d.Books = books.NewService(db, nil, d.Log)
		d.Quality = quality.NewService(db)
		mv = movies.NewService(db, movieMeta{}, nil, root, "", nil, d.Log)
		sr := series.NewService(db, nil, root, d.Log)
		d.Requests = requests.NewService(db, mv, sr, d.Books, nil, d.Quality, nil, "", d.Log)
		d.Requests.SetJobs(noJobs{})
	})
	t.Cleanup(func() { runner.Shutdown(2 * time.Second) })
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	ctx := context.Background()

	// The library already has film 500 with its file.
	have, err := movies.NewRepo(s.st.DB()).Create(ctx, movies.Movie{TMDBID: 500, Title: "On the shelf", Year: 2000, Status: "Released"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.st.DB().Exec(`UPDATE movies SET has_file = 1, movie_file_path = '/movies/x.mkv' WHERE id = ?`, have.ID); err != nil {
		t.Fatal(err)
	}

	row := func(reqStatus, mediaStatus, tmdb int) string {
		return `{"status": ` + strconv.Itoa(reqStatus) + `, "type": "movie", "media": {"tmdbId": ` + strconv.Itoa(tmdb) + `, "mediaType": "movie", "status": ` + strconv.Itoa(mediaStatus) + `}, "requestedBy": {"username": "admin@example.com"}}`
	}
	seerr := fakeSeerr(t, strings.Join([]string{
		row(2, 3, 100), // approved, processing: still wanted
		row(1, 1, 101), // pending
		row(2, 7, 102), // approved, then deleted (Jellyseerr/Seerr)
		row(5, 6, 103), // completed, then deleted (Overseerr)
		row(5, 5, 104), // completed, available, not in this library
		row(2, 5, 500), // approved, available, in this library
		row(3, 1, 105), // declined
	}, ","))

	rec := s.doJSON("POST", "/api/v1/requests/import/overseerr", admin, `{"url":"`+seerr.URL+`","api_key":"k"}`)
	if rec.Code != http.StatusAccepted && rec.Code != http.StatusOK {
		t.Fatalf("import: HTTP %d %s", rec.Code, rec.Body)
	}
	if j := waitJob(t, runner, rec.Result(), rec.Body.Bytes()); j.Status != jobs.StatusSucceeded {
		t.Fatalf("import job: %+v", j)
	}

	got := map[int]string{}
	list, _, err := s.deps.Requests.List(ctx, requests.ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		got[r.TMDBID] = r.Status
	}
	want := map[int]string{100: requests.StatusApproved, 101: requests.StatusPending, 500: requests.StatusApproved}
	if len(got) != len(want) {
		t.Errorf("imported %v, want %v", got, want)
	}
	for tmdb, st := range want {
		if got[tmdb] != st {
			t.Errorf("tmdb %d: %q, want %q", tmdb, got[tmdb], st)
		}
	}
	// Only the still-wanted film joined the library, marked to be searched in its turn.
	marked, err := mv.PacedSearchIDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	added, err := mv.GetByTMDB(ctx, 100)
	if err != nil || !added.Monitored || !marked[added.ID] || len(marked) != 1 {
		t.Errorf("film 100: %+v (%v), paced %v; want it alone monitored and paced", added, err, marked)
	}
	for _, tmdb := range []int{102, 103, 104, 105} {
		if _, err := mv.GetByTMDB(ctx, tmdb); err == nil {
			t.Errorf("film %d was added to the library", tmdb)
		}
	}
}
