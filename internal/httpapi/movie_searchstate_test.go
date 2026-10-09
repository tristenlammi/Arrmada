package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// GET /movies/{id} carries the search state beside the movie: the sweep's backoff, when it
// next looks, and the last search's summary (MOV-04).
func TestGetMovieCarriesSearchState(t *testing.T) {
	s := newRouteServer(t, func(d *Deps) {
		mv := movies.NewService(d.Store.DB(), nil, nil, t.TempDir(), "", nil, d.Log)
		dl := download.NewService(d.Store.DB(), d.Log)
		d.Movies, d.Downloads = mv, dl
		d.Automation = automation.New(mv, nil, dl, nil, d.Store.DB(), nil, d.Log, "")
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	db := s.st.DB()
	m, err := movies.NewRepo(db).Create(t.Context(), movies.Movie{TMDBID: 1, Title: "Arrival", Year: 2016, Monitored: true, MinAvailability: "announced"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE movies SET last_search_at = datetime('now', '-10 minutes'), search_misses = 2 WHERE id = ?`, m.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO search_attempts (media_type, media_id, started_at, returned, wrong_title, outcome, top_reason, reasons_json)
		VALUES ('movie', ?, 1000, 12, 12, 'none_suitable', 'wrong_title', '{"wrong_title":12}')`, m.ID); err != nil {
		t.Fatal(err)
	}

	rec := s.do("GET", "/api/v1/movies/1", mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Title        string                     `json:"title"`
		SearchMisses int                        `json:"search_misses"`
		LastSearchAt string                     `json:"last_search_at"`
		NextSearchAt string                     `json:"next_search_at"`
		LastSearch   *automation.AttemptSummary `json:"last_search"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatal(err)
	}
	if body.Title != "Arrival" || body.SearchMisses != 2 || body.LastSearchAt == "" {
		t.Fatalf("body = %+v", body)
	}
	// Two misses back off an hour from the last sweep search, ten minutes ago.
	next, err := time.Parse(time.RFC3339, body.NextSearchAt)
	if err != nil {
		t.Fatalf("next_search_at %q: %v", body.NextSearchAt, err)
	}
	if d := time.Until(next); d < 40*time.Minute || d > 55*time.Minute {
		t.Fatalf("next search in %v, want about 50 minutes", d)
	}
	if body.LastSearch == nil || body.LastSearch.Latest.Returned != 12 || body.LastSearch.EmptyTries != 1 || body.LastSearch.MainReason != "wrong_title" {
		t.Fatalf("last_search = %+v", body.LastSearch)
	}
}
