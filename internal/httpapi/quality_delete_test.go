package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/quality"
)

// Deleting a profile answers with what moved and where; the last profile of a media type
// is a 409, and a target of another media type a 400 that changes nothing.
func TestDeleteQualityProfileReassigns(t *testing.T) {
	s := newRouteServer(t, func(d *Deps) { d.Quality = quality.NewService(d.Store.DB()) })
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	ctx := context.Background()
	q := s.deps.Quality
	mk := func(media, name string) int64 {
		sp, err := q.Create(ctx, quality.StoredProfile{MediaType: media, Name: name})
		if err != nil {
			t.Fatal(err)
		}
		return sp.ID
	}
	gone := mk(quality.MediaMovie, "Going")
	keep := mk(quality.MediaMovie, "Keeping")
	show := mk(quality.MediaSeries, "Shows")
	db := s.st.DB()
	if _, err := db.Exec(`INSERT INTO movies (tmdb_id, title, quality_profile) VALUES (1, 'Heat', ?), (2, 'Ronin', ?)`,
		fmt.Sprintf("custom:%d", gone), fmt.Sprintf("custom:%d", gone)); err != nil {
		t.Fatal(err)
	}

	rec := s.do("DELETE", fmt.Sprintf("/api/v1/quality/profiles/%d?move_to=custom:%d", gone, show), mgr)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("mismatch: HTTP %d: %s", rec.Code, rec.Body)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM movies WHERE quality_profile = ?`, fmt.Sprintf("custom:%d", gone)).Scan(&n)
	if n != 2 {
		t.Errorf("a refused delete moved movies (%d left)", n)
	}

	rec = s.do("DELETE", fmt.Sprintf("/api/v1/quality/profiles/%d?move_to=custom:%d", gone, keep), mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Moved   quality.Reassigned `json:"moved"`
		MovedTo string             `json:"moved_to"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Moved.Movies != 2 || got.MovedTo != "custom:"+strconv.FormatInt(keep, 10) {
		t.Errorf("body = %+v", got)
	}

	if rec := s.do("DELETE", fmt.Sprintf("/api/v1/quality/profiles/%d", gone), mgr); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: HTTP %d", rec.Code)
	}

	// Delete series profiles down to the one we made, then try that one.
	list, err := q.ListStored(ctx, quality.MediaSeries)
	if err != nil {
		t.Fatal(err)
	}
	for _, sp := range list {
		if sp.ID != show {
			if rec := s.do("DELETE", fmt.Sprintf("/api/v1/quality/profiles/%d", sp.ID), mgr); rec.Code != http.StatusOK {
				t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
			}
		}
	}
	if rec := s.do("DELETE", fmt.Sprintf("/api/v1/quality/profiles/%d", show), mgr); rec.Code != http.StatusConflict {
		t.Errorf("last profile: HTTP %d: %s", rec.Code, rec.Body)
	}
}
