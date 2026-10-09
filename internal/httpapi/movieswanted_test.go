package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
)

func getMoviesWanted(t *testing.T, s *routeServer, c *http.Cookie, tab string, out any) {
	t.Helper()
	rec := s.do("GET", "/api/v1/movies/wanted?tab="+tab, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /movies/wanted?tab=%s: HTTP %d: %s", tab, rec.Code, rec.Body)
	}
	if err := json.Unmarshal(rec.Body.Bytes(), out); err != nil {
		t.Fatal(err)
	}
}

func movieRowFor(rows []movieWantedRow, id int64) (movieWantedRow, bool) {
	for _, r := range rows {
		if r.ID == id {
			return r, true
		}
	}
	return movieWantedRow{}, false
}

// Wanted → Missing lists the monitored films with no file — the ones badged Wanted —
// with their search record, says which are queued, and lists apart the films whose only
// missing track is an extra version. Unmonitored and complete films are left out.
func TestMoviesWantedMissing(t *testing.T) {
	fj := newFakeJobs(false)
	s := wantedServer(t, fj)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	mustExecAll(t, s,
		`INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability, search_misses) VALUES (1, 1, 'Alpha', 2001, 1, 'announced', 2)`,
		`INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability) VALUES (2, 2, 'Bravo', 2002, 0, 'announced')`,
		`INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability, has_file, movie_file_path) VALUES (3, 3, 'Charlie', 2003, 1, 'announced', 1, '/m/c.mkv')`,
		`INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability, has_file, movie_file_path) VALUES (4, 4, 'Delta', 2004, 1, 'announced', 1, '/m/d.mkv')`,
		`INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability) VALUES (5, 5, 'Echo', 2005, 1, 'announced')`,
	)
	if _, err := s.deps.Movies.AddVersion(context.Background(), 4, "4K", "", "", true); err != nil {
		t.Fatal(err)
	}
	// Echo's search waits in the movie search queue (the fake runner never runs it).
	if rec := s.do("POST", "/api/v1/movies/5/search", mgr); rec.Code != http.StatusAccepted {
		t.Fatalf("search: HTTP %d", rec.Code)
	}

	var got movieMissing
	getMoviesWanted(t, s, mgr, "missing", &got)
	if len(got.Searching) != 2 {
		t.Fatalf("searching = %+v, want Alpha and Echo", got.Searching)
	}
	alpha, ok := movieRowFor(got.Searching, 1)
	if !ok || alpha.State != wantedSearching || alpha.SearchMisses != 2 || alpha.NextSearchAt == "" || alpha.Queued {
		t.Errorf("Alpha = %+v (listed %v)", alpha, ok)
	}
	if echo, ok := movieRowFor(got.Searching, 5); !ok || !echo.Queued {
		t.Errorf("Echo = %+v (listed %v), want queued", echo, ok)
	}
	for _, id := range []int64{2, 3, 4} {
		if _, ok := movieRowFor(got.Searching, id); ok {
			t.Errorf("movie %d listed as wanted", id)
		}
	}
	if len(got.Versions) != 1 || got.Versions[0].ID != 4 || len(got.Versions[0].Tracks) != 1 || got.Versions[0].Tracks[0] != "4K" {
		t.Errorf("versions = %+v, want Delta's 4K", got.Versions)
	}
	// Staff only.
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)
	if rec := s.do("GET", "/api/v1/movies/wanted", kid); rec.Code != http.StatusForbidden {
		t.Errorf("requester: HTTP %d", rec.Code)
	}
}

// Wanted → Cutoff unmet lists the files that miss their profile's target and says whether
// the upgrade sweep will act: a monitored film on an upgrading profile will; a film a scan
// catalogued (profile n/a, unmonitored) won't, and says it was scanned in.
func TestMoviesWantedCutoff(t *testing.T) {
	var q *quality.Service
	s := newRouteServer(t, func(d *Deps) {
		db := d.Store.DB()
		mv := movies.NewService(db, nil, nil, t.TempDir(), "", nil, d.Log)
		dl := download.NewService(db, d.Log)
		q = quality.NewService(db)
		d.Movies, d.Downloads, d.Quality = mv, dl, q
		d.Automation = automation.New(mv, nil, dl, q, db, nil, d.Log, "")
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	ctx := context.Background()
	sp, err := q.Create(ctx, quality.StoredProfile{MediaType: quality.MediaMovie, Name: "HEVC 1080p", AllowedResolutions: []string{"1080p"},
		UpgradesEnabled: true, Ideal: &quality.IdealFile{Codec: map[string]string{"hevc": quality.PrefMust}}})
	if err != nil {
		t.Fatal(err)
	}
	ref := "custom:" + strconv.FormatInt(sp.ID, 10)
	if err := q.SetDefaultProfile(ctx, quality.MediaMovie, ref); err != nil {
		t.Fatal(err)
	}
	mustExecAll(t, s,
		`INSERT INTO movies (id, tmdb_id, title, year, monitored, quality_profile, has_file, movie_file_path, source_release)
			VALUES (1, 1, 'Alpha', 2001, 1, '`+ref+`', 1, '/m/a.mkv', 'Alpha.2001.1080p.BluRay.x264-GRP')`,
		`INSERT INTO movies (id, tmdb_id, title, year, monitored, quality_profile, has_file, movie_file_path, source_release)
			VALUES (2, 2, 'Bravo', 2002, 0, 'n/a', 1, '/m/b.mkv', 'Bravo.2002.1080p.BluRay.x264-GRP')`,
		`INSERT INTO movies (id, tmdb_id, title, year, monitored, quality_profile) VALUES (3, 3, 'Charlie', 2003, 1, '`+ref+`')`,
	)
	var got struct {
		Rows []movieWantedRow `json:"rows"`
	}
	getMoviesWanted(t, s, mgr, "cutoff", &got)
	if len(got.Rows) != 2 {
		t.Fatalf("rows = %+v, want Alpha and Bravo", got.Rows)
	}
	alpha, _ := movieRowFor(got.Rows, 1)
	if !alpha.WillUpgrade || alpha.State != wantedUpgrading || len(alpha.Issues) == 0 || alpha.QualityProfile != "HEVC 1080p" {
		t.Errorf("Alpha = %+v, want an upgrade coming for its codec", alpha)
	}
	bravo, _ := movieRowFor(got.Rows, 2)
	if bravo.WillUpgrade || bravo.State != wantedNotUpgrading || bravo.WhyNot != "Scanned in — not monitored" {
		t.Errorf("Bravo = %+v, want no upgrade, scanned in", bravo)
	}
}

// Search all queues every title through the movie search queue — once each, however often
// it is listed or clicked — and an upgrade Search all is its own kind of job.
func TestBulkSearchEnqueues(t *testing.T) {
	fj := newFakeJobs(false)
	s := wantedServer(t, fj)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	post := func(body string) (int, map[string]any) {
		rec := s.doJSON("POST", "/api/v1/movies/search", mgr, body)
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return rec.Code, out
	}
	if code, out := post(`{"ids":[1,2,2,0],"kind":"missing"}`); code != http.StatusAccepted || out["queued"] != float64(2) || out["duplicates"] != float64(0) {
		t.Fatalf("first: HTTP %d %v", code, out)
	}
	if code, out := post(`{"ids":[1,2],"kind":"missing"}`); code != http.StatusAccepted || out["queued"] != float64(0) || out["duplicates"] != float64(2) {
		t.Fatalf("again: HTTP %d %v", code, out)
	}
	if code, out := post(`{"ids":[1],"kind":"upgrade"}`); code != http.StatusAccepted || out["queued"] != float64(1) {
		t.Fatalf("upgrade: HTTP %d %v", code, out)
	}
	specs := fj.specs()
	if len(specs) != 3 || specs[0].Kind != "movie.search" || specs[2].Kind != "movie.upgrade" || specs[2].Target != "movie:1" {
		t.Fatalf("specs = %+v", specs)
	}
	for _, body := range []string{`{"ids":[1],"kind":"everything"}`, `{"ids":[],"kind":"missing"}`, `{"ids":[0,-1],"kind":"missing"}`} {
		if code, _ := post(body); code != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400", body, code)
		}
	}
	if got := s.deps.Automation.MovieSearchQueue(); len(got.Queued) != 3 {
		t.Errorf("queue = %+v, want three searches waiting", got)
	}
}
