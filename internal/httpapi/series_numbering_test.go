package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/series"
)

// The review endpoints: the pending proposal reads back as a remap table, an Apply of a
// plan that isn't the pending one is a 409 that moves nothing, and Dismiss hides it.
func TestSeriesNumberingReviewEndpoints(t *testing.T) {
	s := newRouteServer(t, func(d *Deps) {
		d.Series = series.NewService(d.Store.DB(), nil, t.TempDir(), d.Log)
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	db := s.st.DB()
	res, err := db.Exec(`INSERT INTO series (tmdb_id, title, monitored, series_type, numbering_source) VALUES (9, 'Anime', 1, 'anime', 'tmdb')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()

	rec := s.do("GET", fmt.Sprintf("/api/v1/series/%d/numbering", id), mgr)
	if rec.Code != http.StatusOK || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var empty struct {
		Source  string           `json:"source"`
		Pending *json.RawMessage `json:"pending"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &empty)
	if empty.Source != "tmdb" || empty.Pending != nil {
		t.Fatalf("no proposal yet: %s", rec.Body)
	}

	remaps := `[{"absolute":33,"old_season":2,"old_episode":33,"new_season":3,"new_episode":1,"file_path":"/tv/Anime/Season 2/Anime - S02E33.mkv"},` +
		`{"absolute":34,"old_season":2,"old_episode":34,"new_season":0,"new_episode":0,"file_path":"/tv/Anime/Season 2/Anime - S02E34.mkv","unplaced":true}]`
	if _, err := db.Exec(`INSERT INTO series_numbering_pending (series_id, from_source, to_source, plan_hash, remaps_json) VALUES (?, 'tmdb', 'tvdb', 'abc', ?)`, id, remaps); err != nil {
		t.Fatal(err)
	}
	rec = s.do("GET", fmt.Sprintf("/api/v1/series/%d/numbering", id), mgr)
	var got struct {
		Pending *struct {
			From, To, PlanHash string
			Files              int
			Remaps             []struct {
				Absolute       int
				Old, New, File string
			}
		} `json:"pending"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || got.Pending == nil {
		t.Fatalf("pending: %s (%v)", rec.Body, err)
	}
	p := got.Pending
	if p.From != "tmdb" || p.To != "tvdb" || p.Files != 1 || len(p.Remaps) != 2 {
		t.Fatalf("pending = %+v", p)
	}
	if r := p.Remaps[0]; r.Old != "S02E33" || r.New != "S03E01" || r.File != "Anime - S02E33.mkv" {
		t.Errorf("row = %+v, want S02E33 → S03E01 with the file's base name", r)
	}
	if r := p.Remaps[1]; r.New != "" {
		t.Errorf("an unplaced file has no new episode: %+v", r)
	}

	// A plan that isn't the one pending is refused before anything runs.
	rec = s.doJSON("POST", fmt.Sprintf("/api/v1/series/%d/numbering/apply", id), mgr, `{"plan_hash":"other"}`)
	if rec.Code != http.StatusConflict {
		t.Fatalf("stale apply: HTTP %d: %s", rec.Code, rec.Body)
	}

	rec = s.do("DELETE", fmt.Sprintf("/api/v1/series/%d/numbering/pending", id), mgr)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("dismiss: HTTP %d: %s", rec.Code, rec.Body)
	}
	rec = s.do("GET", fmt.Sprintf("/api/v1/series/%d/numbering", id), mgr)
	_ = json.Unmarshal(rec.Body.Bytes(), &empty)
	if empty.Pending != nil {
		t.Errorf("a dismissed proposal is hidden: %s", rec.Body)
	}
	if rec := s.do("DELETE", fmt.Sprintf("/api/v1/series/%d/numbering/pending", id), mgr); rec.Code != http.StatusNotFound {
		t.Errorf("dismissing nothing: HTTP %d", rec.Code)
	}
}
