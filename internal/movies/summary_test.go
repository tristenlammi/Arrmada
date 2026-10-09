package movies

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestSortTitle(t *testing.T) {
	for in, want := range map[string]string{
		"The Matrix":    "matrix",
		"Amélie":        "amelie",
		"A Quiet Place": "quiet place",
		"An Education":  "education",
		"Anora":         "anora", // "An" only as a whole word
		"The":           "the",   // nothing would be left
		"  Heat ":       "heat",
	} {
		if got := SortTitle(in); got != want {
			t.Errorf("SortTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// The library list carries what the grid and the table show, and none of the heavy or
// private parts of a movie: no cast, overview, genres or file paths.
func TestSummaryJSONOmitsExtra(t *testing.T) {
	s, ctx := testService(t)
	db := s.repo.db
	extra := `{"genres":["Drama"],"vote_average":7.9,"overview_note":"x","cast":[{"name":"Someone Famous","profile_url":"https://image.tmdb.org/p.jpg"}]}`
	media := `{"path":"/media/movies/Heat (1995)/Heat.mkv","filename":"Heat.mkv","size_bytes":8000000000,"resolution":"1080p","codec":"x265","audio":["DTS-HD MA"],"hdr":["HDR10"],"duration_min":170,"v":2}`
	if _, err := db.Exec(`INSERT INTO movies (id, tmdb_id, title, year, overview, monitored, has_file, movie_file_path, extra_json, media_json)
		VALUES (1, 949, 'Heat', 1995, 'A secret plot overview', 1, 1, '/media/movies/Heat (1995)/Heat.mkv', ?, ?)`, extra, media); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSummaries(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("summaries = %+v, %v", list, err)
	}
	m := list[0]
	if m.SortTitle != "heat" || m.VoteAverage != 7.9 || m.SizeBytes != 8000000000 || m.Media == nil ||
		m.Media.Resolution != "1080p" || m.Media.Container != "mkv" || m.Media.BitrateMbps < 6 || m.MediaStale() {
		t.Fatalf("summary = %+v media %+v", m, m.Media)
	}
	b, _ := json.Marshal(list)
	for _, leak := range []string{"cast", "Someone Famous", "overview", "secret plot", "genres", "/media/movies", "path"} {
		if strings.Contains(string(b), leak) {
			t.Errorf("summary JSON carries %q: %s", leak, b)
		}
	}
}

// A film scanned in from disk has no TMDB extras (an empty extra_json). json_extract fails on
// that, which once made the whole library list answer 500.
func TestSummariesListFilmsWithoutExtras(t *testing.T) {
	s, ctx := testService(t)
	if _, err := s.repo.db.Exec(`INSERT INTO movies (id, tmdb_id, title, year, monitored, extra_json)
		VALUES (1, 949, 'Heat', 1995, 0, ''), (2, 950, 'Ronin', 1998, 1, '{"vote_average":7}')`); err != nil {
		t.Fatal(err)
	}
	list, err := s.ListSummaries(ctx)
	if err != nil || len(list) != 2 {
		t.Fatalf("summaries = %+v, %v", list, err)
	}
	for _, m := range list {
		if m.Title == "Heat" && m.VoteAverage != 0 || m.Title == "Ronin" && m.VoteAverage != 7 {
			t.Errorf("%s vote = %v", m.Title, m.VoteAverage)
		}
	}
}
