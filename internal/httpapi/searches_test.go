package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/movies"
)

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
