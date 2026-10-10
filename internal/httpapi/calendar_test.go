package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/series"
)

// calendarServer is a route server with real movie, series and requests services.
func calendarServer(t *testing.T) *routeServer {
	t.Helper()
	root := t.TempDir()
	return newRouteServer(t, func(d *Deps) {
		db := d.Store.DB()
		d.Books = books.NewService(db, nil, d.Log)
		d.Quality = quality.NewService(db)
		d.Movies = movies.NewService(db, nil, nil, root, "", nil, d.Log)
		d.Series = series.NewService(db, nil, root, d.Log)
		d.Requests = requests.NewService(db, d.Movies, d.Series, d.Books, nil, d.Quality, nil, "", d.Log)
	})
}

type calendarBody struct {
	Items []CalendarItem `json:"items"`
	Start string         `json:"start"`
	End   string         `json:"end"`
}

func getCalendar(t *testing.T, s *routeServer, path string, c *http.Cookie) calendarBody {
	t.Helper()
	rec := s.do("GET", path, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: HTTP %d: %s", path, rec.Code, rec.Body)
	}
	var b calendarBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// seedCalendar puts two library series (TMDB 501, 502) with an episode each and two
// movies (TMDB 601, 602) inside October 2026, plus one episode outside the window.
func seedCalendar(t *testing.T, s *routeServer) {
	t.Helper()
	for _, q := range []string{
		`INSERT INTO series (id, tmdb_id, title, monitored) VALUES (11, 501, 'Harbour Lights', 1), (12, 502, 'Saltwind', 0)`,
		`INSERT INTO episodes (series_id, season_number, episode_number, title, air_date, monitored, has_file) VALUES
			(11, 2, 5, 'The Breakwater', '2026-10-09', 1, 0),
			(12, 1, 1, 'Pilot', '2026-10-12', 1, 1),
			(11, 2, 6, 'Too Late', '2026-12-01', 1, 0)`,
		`INSERT INTO movies (id, tmdb_id, title, year, monitored, has_file, extra_json) VALUES
			(21, 601, 'The Cartographer', 2026, 1, 1, '{"release_date":"2026-10-10"}'),
			(22, 602, 'Iron Tide', 2026, 1, 0, '{"release_date":"2026-10-20"}')`,
	} {
		if _, err := s.st.DB().Exec(q); err != nil {
			t.Fatal(err)
		}
	}
}

// Every item carries what a requester's tap needs (the title's TMDB id and kind) and the
// episode's numbers and name as fields, so nothing has to be read out of a tooltip.
func TestCalendarIncludesTMDBID(t *testing.T) {
	s := calendarServer(t)
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)
	seedCalendar(t, s)

	b := getCalendar(t, s, "/api/v1/calendar?start=2026-10-01&end=2026-10-31", kid)
	if len(b.Items) != 4 {
		t.Fatalf("items = %+v, want the 4 dated in October", b.Items)
	}
	byTMDB := map[int]CalendarItem{}
	for _, it := range b.Items {
		byTMDB[it.TMDBID] = it
	}
	ep := byTMDB[501]
	if ep.MediaType != "series" || ep.Type != "episode" || ep.RefID != 11 || ep.Season != 2 || ep.Episode != 5 ||
		ep.EpisodeTitle != "The Breakwater" || ep.Date != "2026-10-09" {
		t.Errorf("episode = %+v", ep)
	}
	if ep.Subtitle == "" {
		t.Error("the old subtitle is gone; older clients still read it")
	}
	mv := byTMDB[602]
	if mv.MediaType != "movie" || mv.Type != "movie" || mv.RefID != 22 || mv.Year != 2026 || mv.Date != "2026-10-20" {
		t.Errorf("movie = %+v", mv)
	}
	if _, ok := byTMDB[0]; ok {
		t.Errorf("an item without a TMDB id: %+v", b.Items)
	}
}
