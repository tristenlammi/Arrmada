package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/series"
)

// reviewServer is the real router with a real coordinator and a job runner that records.
func reviewServer(t *testing.T) (*routeServer, *fakeJobs, *http.Cookie) {
	t.Helper()
	fj := newFakeJobs(false)
	s := newRouteServer(t, func(d *Deps) {
		mv := movies.NewService(d.Store.DB(), nil, nil, t.TempDir(), "", nil, d.Log)
		d.Movies = mv
		d.Automation = automation.New(mv, nil, nil, nil, d.Store.DB(), nil, d.Log, "")
		d.Jobs = fj
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	return s, fj, mgr
}

func addReviewRow(t *testing.T, s *routeServer, hash, mediaType string, expectedID int64, code, contentPath string) int64 {
	t.Helper()
	res, err := s.st.DB().Exec(`INSERT INTO import_reviews (hash, name, content_path, media_type, expected_id, expected_title, reason, reason_code)
		VALUES (?, ?, ?, ?, ?, 'Arrival', 'held', ?)`, hash, "Release."+hash, contentPath, mediaType, expectedID, code)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

// Reject & find another blocklists the release and starts one search for the title it
// was grabbed for; plain Reject starts none.
func TestRejectReviewFindAnother(t *testing.T) {
	s, fj, mgr := reviewServer(t)
	res, err := s.st.DB().Exec(`INSERT INTO movies (tmdb_id, title, year, monitored) VALUES (1, 'Arrival', 2016, 1)`)
	if err != nil {
		t.Fatal(err)
	}
	mid, _ := res.LastInsertId()
	plain := addReviewRow(t, s, "aa", "movie", mid, "mismatch", "")
	again := addReviewRow(t, s, "bb", "movie", mid, "mismatch", "")

	if rec := s.doJSON("POST", fmt.Sprintf("/api/v1/reviews/%d/reject", plain), mgr, ""); rec.Code != http.StatusOK {
		t.Fatalf("reject: HTTP %d: %s", rec.Code, rec.Body)
	}
	if n := len(fj.specs()); n != 0 {
		t.Fatalf("plain reject started %d job(s)", n)
	}
	rec := s.doJSON("POST", fmt.Sprintf("/api/v1/reviews/%d/reject", again), mgr, `{"find_another": true}`)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("reject & find another: HTTP %d: %s", rec.Code, rec.Body)
	}
	specs := fj.specs()
	if len(specs) != 1 || specs[0].Kind != "movie.search" || specs[0].Target != fmt.Sprintf("movie:%d", mid) {
		t.Fatalf("jobs = %+v, want one movie.search for the movie", specs)
	}
	var n int
	_ = s.st.DB().QueryRow(`SELECT COUNT(*) FROM blocklist WHERE movie_id = ? AND media_type = 'movie'`, mid).Scan(&n)
	if n != 2 {
		t.Errorf("blocklist rows = %d, want both rejected releases", n)
	}
	// Rejecting the same review again finds nothing to act on.
	if rec := s.doJSON("POST", fmt.Sprintf("/api/v1/reviews/%d/reject", again), mgr, ""); rec.Code != http.StatusNotFound {
		t.Errorf("second reject: HTTP %d, want 404", rec.Code)
	}
}

// Bulk Dismiss settles several reviews at once and says which it couldn't.
func TestBulkDismissReviews(t *testing.T) {
	s, _, mgr := reviewServer(t)
	a := addReviewRow(t, s, "aa", "movie", 1, "mismatch", "")
	b := addReviewRow(t, s, "bb", "series", 2, "numbering", "")
	rec := s.doJSON("POST", "/api/v1/reviews/bulk", mgr, fmt.Sprintf(`{"ids":[%d,%d,999],"action":"dismiss"}`, a, b))
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk: HTTP %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Done   int `json:"done"`
		Failed []struct {
			ID int64 `json:"id"`
		} `json:"failed"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Done != 2 || len(body.Failed) != 1 || body.Failed[0].ID != 999 {
		t.Errorf("bulk result = %+v", body)
	}
	var pending int
	_ = s.st.DB().QueryRow(`SELECT COUNT(*) FROM import_reviews WHERE status = 'pending'`).Scan(&pending)
	if pending != 0 {
		t.Errorf("%d review(s) still pending", pending)
	}
	if rec := s.doJSON("POST", "/api/v1/reviews/bulk", mgr, `{"ids":[1],"action":"import"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown bulk action: HTTP %d, want 400", rec.Code)
	}
}

// Map files only works on a download inside the downloads or library folders — the manual
// import rule — and refuses a bad mapping with a 422 before touching anything.
func TestMapReviewRoute(t *testing.T) {
	base := t.TempDir()
	downloads := filepath.Join(base, "downloads")
	outside := filepath.Join(base, "elsewhere")
	for _, d := range []string{downloads, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	s := newRouteServer(t, func(d *Deps) {
		d.Config.DownloadsDir = downloads
		co := automation.New(nil, nil, nil, nil, d.Store.DB(), nil, d.Log, "")
		co.SetSeries(series.NewService(d.Store.DB(), nil, filepath.Join(base, "tv"), d.Log), library.NewImporter(filepath.Join(base, "tv"), d.Log))
		d.Automation = co
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	inside := filepath.Join(downloads, "Show.S01")
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	away := addReviewRow(t, s, "aa", "series", 1, "numbering", outside)
	in := addReviewRow(t, s, "bb", "series", 1, "numbering", inside)
	body := `{"files":[{"rel_path":"../x.mkv","season":1,"episodes":[1]}]}`
	if rec := s.doJSON("POST", fmt.Sprintf("/api/v1/reviews/%d/map", away), mgr, body); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("outside the roots: HTTP %d, want 422: %s", rec.Code, rec.Body)
	}
	rec := s.doJSON("POST", fmt.Sprintf("/api/v1/reviews/%d/map", in), mgr, body)
	if rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("bad mapping: HTTP %d: %s", rec.Code, rec.Body)
	}
}

// The file list and Retry import answer through the router; Retry on the wrong kind of
// review is the user's to fix (422).
func TestReviewFilesAndRetryRoutes(t *testing.T) {
	s, _, mgr := reviewServer(t)
	dl := t.TempDir()
	if err := os.WriteFile(filepath.Join(dl, "Show.S01E01.mkv"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	id := addReviewRow(t, s, "aa", "series", 1, "numbering", dl)
	rec := s.do("GET", fmt.Sprintf("/api/v1/reviews/%d/files", id), mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("files: HTTP %d: %s", rec.Code, rec.Body)
	}
	var files struct {
		Files []automation.ReviewFile `json:"files"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &files)
	if len(files.Files) != 1 || files.Files[0].RelPath != "Show.S01E01.mkv" || files.Files[0].Guess.Season != 1 {
		t.Errorf("files = %+v", files.Files)
	}
	if rec := s.do("POST", fmt.Sprintf("/api/v1/reviews/%d/retry", id), mgr); rec.Code != http.StatusUnprocessableEntity {
		t.Errorf("retry on a numbering review: HTTP %d, want 422", rec.Code)
	}
	failed := addReviewRow(t, s, "bb", "movie", 1, "import_failed", "")
	if rec := s.do("POST", fmt.Sprintf("/api/v1/reviews/%d/retry", failed), mgr); rec.Code != http.StatusOK {
		t.Errorf("retry: HTTP %d: %s", rec.Code, rec.Body)
	}
}
