package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// Search now answers with when it started, so a page can find its stored attempt.
func TestSearchNowSaysWhenItStarted(t *testing.T) {
	fj := newFakeJobs(false)
	s := newRouteServer(t, func(d *Deps) { d.Jobs = fj })
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	for _, path := range []string{"/api/v1/movies/5/search", "/api/v1/series/6/search", "/api/v1/books/7/search"} {
		rec := s.do("POST", path, mgr)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: HTTP %d: %s", path, rec.Code, rec.Body)
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		if ms, _ := body["started_at_ms"].(float64); ms <= 0 {
			t.Errorf("%s: body = %v, want started_at_ms", path, body)
		}
	}
}

// GET /api/v1/searches lists a title's search attempts, newest first, to staff only.
func TestListSearches(t *testing.T) {
	s := newRouteServer(t, func(d *Deps) {
		mv := movies.NewService(d.Store.DB(), nil, nil, t.TempDir(), "", nil, d.Log)
		d.Movies = mv
		d.Automation = automation.New(mv, nil, nil, nil, d.Store.DB(), nil, d.Log, "")
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)
	db := s.st.DB()
	for i, started := range []int64{1000, 2000, 3000} {
		if _, err := db.Exec(`INSERT INTO search_attempts (media_type, media_id, started_at, returned, outcome, reasons_json)
			VALUES ('movie', 4, ?, ?, 'none_suitable', '{"wrong_title":2}')`, started, i); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := db.Exec(`INSERT INTO search_attempts (media_type, media_id, started_at, outcome) VALUES ('series', 4, 5000, 'grabbed')`); err != nil {
		t.Fatal(err)
	}

	rec := s.do("GET", "/api/v1/searches?kind=movie&id=4&since=2000", mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Attempts []automation.Attempt `json:"attempts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Attempts) != 2 || body.Attempts[0].StartedAt != 3000 || body.Attempts[1].StartedAt != 2000 {
		t.Fatalf("attempts = %+v", body.Attempts)
	}
	if body.Attempts[0].Reasons["wrong_title"] != 2 || body.Attempts[0].MediaType != "movie" {
		t.Fatalf("attempt = %+v", body.Attempts[0])
	}

	for _, bad := range []string{"/api/v1/searches?kind=film&id=4", "/api/v1/searches?kind=movie", "/api/v1/searches?kind=movie&id=4&since=x", "/api/v1/searches?kind=movie&id=4&limit=0"} {
		if rec := s.do("GET", bad, mgr); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400", bad, rec.Code)
		}
	}
	if rec := s.do("GET", "/api/v1/searches?kind=movie&id=4", kid); rec.Code != http.StatusForbidden {
		t.Errorf("a requester read search attempts: HTTP %d", rec.Code)
	}
}

// SER-19: the series page's season and episode buttons read their own scope's last
// search from the server ("S03", "S03E04"; the whole show is ""), so a Grab that found
// nothing still says so after a reload and on another device.
func TestSeriesSearchesKeepTheirScope(t *testing.T) {
	s := newRouteServer(t, func(d *Deps) {
		mv := movies.NewService(d.Store.DB(), nil, nil, t.TempDir(), "", nil, d.Log)
		d.Movies = mv
		d.Automation = automation.New(mv, nil, nil, nil, d.Store.DB(), nil, d.Log, "")
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	if _, err := s.st.DB().Exec(`INSERT INTO search_attempts (media_type, media_id, scope, started_at, returned, outcome, top_reason, reasons_json)
		VALUES ('series', 9, '', 1000, 0, 'nothing_found', '', '{}'),
		       ('series', 9, 'S03', 2000, 37, 'none_suitable', 'wrong_title', '{"wrong_title":22,"bitrate_ceiling":15}'),
		       ('series', 9, 'S03E04', 3000, 0, 'indexers_failed', '', '{}')`); err != nil {
		t.Fatal(err)
	}
	rec := s.do("GET", "/api/v1/searches?kind=series&id=9&limit=20", mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Attempts []automation.Attempt `json:"attempts"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	byScope := map[string]automation.Attempt{}
	for _, a := range body.Attempts {
		byScope[a.Scope] = a
	}
	if len(byScope) != 3 {
		t.Fatalf("scopes = %v, want the show, S03 and S03E04", byScope)
	}
	if a := byScope["S03"]; a.Outcome != automation.OutcomeNoneSuitable || a.Returned != 37 || a.Reasons["wrong_title"] != 22 {
		t.Errorf("S03 = %+v, want 37 found with their reasons", a)
	}
	if a := byScope["S03E04"]; a.Outcome != automation.OutcomeIndexersFailed {
		t.Errorf("S03E04 = %+v, want the indexer outage", a)
	}
}
