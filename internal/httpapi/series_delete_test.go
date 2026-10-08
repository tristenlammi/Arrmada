package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/series"
)

// seriesDeleteServer has one show with one synthetic episode file in a temp library.
func seriesDeleteServer(t *testing.T, bin string) (*routeServer, *http.Cookie, int64, string) {
	t.Helper()
	root := t.TempDir()
	s := newRouteServer(t, func(d *Deps) {
		svc := series.NewService(d.Store.DB(), nil, root, d.Log)
		svc.SetRecycleDir(bin)
		d.Series = svc
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	db := s.st.DB()
	res, err := db.Exec(`INSERT INTO series (tmdb_id, title, monitored) VALUES (9, 'Show: Part 2', 1)`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO seasons (series_id, season_number) VALUES (?, 1)`, id); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO episodes (series_id, season_number, episode_number) VALUES (?, 1, 1)`, id); err != nil {
		t.Fatal(err)
	}
	video := filepath.Join(root, "Show (2020)", "Season 1", "Show - S01E01.mkv")
	if err := os.MkdirAll(filepath.Dir(video), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(video, []byte("0123456789"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := s.deps.Series.MarkEpisodeImported(context.Background(), id, 1, 1, video, 10); err != nil {
		t.Fatal(err)
	}
	return s, mgr, id, video
}

// The preview says how much would move and where it goes.
func TestSeriesDeletePreview(t *testing.T) {
	s, mgr, id, _ := seriesDeleteServer(t, filepath.Join(t.TempDir(), "bin"))
	rec := s.do("GET", fmt.Sprintf("/api/v1/series/%d/delete-preview", id), mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var got struct {
		Files, Sidecars int
		Bytes           int64
		Recycle         struct{ Enabled bool }
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Files != 1 || got.Bytes != 10 || !got.Recycle.Enabled {
		t.Errorf("preview = %+v", got)
	}
}

// A bin that refuses gives 409 with what moved and what failed, and the show stays.
func TestDeleteSeriesReturns409OnRecycleFailure(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "bin")
	if err := os.WriteFile(bin, []byte("not a folder"), 0o644); err != nil {
		t.Fatal(err)
	}
	s, mgr, id, video := seriesDeleteServer(t, bin)
	rec := s.do("DELETE", fmt.Sprintf("/api/v1/series/%d?delete_files=true", id), mgr)
	if rec.Code != http.StatusConflict {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	var body struct {
		Message string
		Moved   []string
		Failed  []string
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.Message == "" || len(body.Failed) == 0 || body.Moved == nil {
		t.Errorf("409 body = %s", rec.Body)
	}
	if _, err := os.Stat(video); err != nil {
		t.Error("file must be untouched")
	}
	if _, err := s.deps.Series.Get(context.Background(), id); err != nil {
		t.Error("series must still exist")
	}
}

// Over the size threshold the title must be typed; the API enforces it.
func TestDeleteSeriesBigNeedsTitle(t *testing.T) {
	old := bigSeriesDeleteBytes
	bigSeriesDeleteBytes = 5 // our 10-byte episode counts as "big"
	t.Cleanup(func() { bigSeriesDeleteBytes = old })

	s, mgr, id, video := seriesDeleteServer(t, filepath.Join(t.TempDir(), "bin"))
	for _, q := range []string{"", "&confirm=Show", "&confirm=show%3A+part+2"} {
		if rec := s.do("DELETE", fmt.Sprintf("/api/v1/series/%d?delete_files=true%s", id, q), mgr); rec.Code != http.StatusBadRequest {
			t.Errorf("confirm %q: HTTP %d, want 400", q, rec.Code)
		}
	}
	if _, err := os.Stat(video); err != nil {
		t.Fatal("refused delete touched the file")
	}
	// With the exact title typed, it goes ahead.
	rec := s.do("DELETE", fmt.Sprintf("/api/v1/series/%d?delete_files=true&confirm=%s", id, url.QueryEscape("Show: Part 2")), mgr)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("typed title: HTTP %d: %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(video); !os.IsNotExist(err) {
		t.Error("the file should be in the bin now")
	}
}
