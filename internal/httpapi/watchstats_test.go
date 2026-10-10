package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/settings"
)

// Watched by is staff information: a requester never sees who watched what. With Plex
// not set up the answer says so (the page then shows nothing).
func TestWatchStatsRoutes(t *testing.T) {
	s := newRouteServer(t, func(d *Deps) {
		d.Movies = movies.NewService(d.Store.DB(), nil, nil, t.TempDir(), "", nil, d.Log)
		d.Insights = insights.NewService(d.Store.DB(), settings.NewService(d.Store.DB()), nil, nil, d.Log)
	})
	res, err := s.st.DB().ExecContext(context.Background(), `INSERT INTO movies (tmdb_id, title, year, monitored) VALUES (949, 'Heat', 1995, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	path := "/api/v1/movies/" + strconv.FormatInt(id, 10) + "/watch-stats"

	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)
	if rec := s.do("GET", path, kid); rec.Code != http.StatusForbidden {
		t.Fatalf("requester: %d, want 403", rec.Code)
	}
	if rec := s.do("GET", "/api/v1/series/1/watch-stats", kid); rec.Code != http.StatusForbidden {
		t.Fatalf("requester on a series: %d, want 403", rec.Code)
	}
	_, boss := s.user(t, "boss@example.com", auth.RoleManager)
	rec := s.do("GET", path, boss)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager: %d %s", rec.Code, rec.Body)
	}
	var ws insights.WatchStats
	if err := json.Unmarshal(rec.Body.Bytes(), &ws); err != nil || ws.Available || ws.Users == nil {
		t.Fatalf("stats without Plex = %s (%v)", rec.Body, err)
	}
	if rec := s.do("GET", "/api/v1/movies/99999/watch-stats", boss); rec.Code != http.StatusNotFound {
		t.Errorf("unknown movie: %d", rec.Code)
	}
}
