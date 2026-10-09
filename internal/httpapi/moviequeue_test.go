package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
)

// Movie searches go through the movie search queue: the answer says where each one
// waits, a second click joins the first, and GET /movies/search-queue lists them.
func TestMovieSearchesAreQueued(t *testing.T) {
	fj := newFakeJobs(false) // records without running: everything stays queued
	s := newRouteServer(t, func(d *Deps) {
		d.Jobs = fj
		d.Automation = &automation.Coordinator{}
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	post := func(path string) map[string]any {
		t.Helper()
		rec := s.do("POST", path, mgr)
		if rec.Code != http.StatusAccepted {
			t.Fatalf("%s: HTTP %d %s", path, rec.Code, rec.Body)
		}
		var b map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		return b
	}
	a := post("/api/v1/movies/3/search")
	b := post("/api/v1/movies/4/search")
	again := post("/api/v1/movies/3/search")
	if a["queued"] != true || a["position"] != float64(1) || b["position"] != float64(2) {
		t.Fatalf("answers = %v / %v", a, b)
	}
	if again["existing"] != true || again["job_id"] != a["job_id"] || again["position"] != float64(1) {
		t.Fatalf("second click = %v, want the first search back", again)
	}
	if n := len(fj.specs()); n != 2 {
		t.Fatalf("%d jobs submitted, want 2", n)
	}
	rec := s.do("GET", "/api/v1/movies/search-queue", mgr)
	var q automation.MovieQueueState
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &q) != nil {
		t.Fatalf("queue: HTTP %d %s", rec.Code, rec.Body)
	}
	if len(q.Running) != 0 || len(q.Queued) != 2 || q.Queued[0].ID != 3 || q.Queued[1].ID != 4 || q.Queued[0].Kind != "search" {
		t.Fatalf("queue = %+v", q)
	}
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)
	if rec := s.do("GET", "/api/v1/movies/search-queue", kid); rec.Code != http.StatusForbidden {
		t.Fatalf("requester: HTTP %d, want 403", rec.Code)
	}
}
