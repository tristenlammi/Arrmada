package httpapi

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/convert"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

// "Don't convert this" through the API: on, listed, off; a title that isn't in the library
// or a request naming nothing is refused. The resolution rule saves through the settings.
func TestConvertExclusionsAPI(t *testing.T) {
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	dir := t.TempDir()
	mv := movies.NewService(db, nil, nil, dir, "", nil, log)
	cv := convert.NewService(db, mv, nil, settings.NewService(db), filepath.Join(dir, "no-ffmpeg"), filepath.Join(dir, "no-ffprobe"),
		filepath.Join(dir, "scratch"), filepath.Join(dir, "recycle"), log)
	a := &api{deps: Deps{Store: st, Log: log, Movies: mv, Convert: cv}}
	if _, err := db.Exec(`INSERT INTO movies (id, tmdb_id, title, year, monitored, min_availability) VALUES (1, 1, 'Aliens', 1986, 1, 'announced')`); err != nil {
		t.Fatal(err)
	}

	put := func(body string) *httptest.ResponseRecorder {
		rec := httptest.NewRecorder()
		a.handleConvertExclusionSet(rec, httptest.NewRequest(http.MethodPut, "/api/v1/convert/exclusions", strings.NewReader(body)))
		return rec
	}
	list := func() []convert.TitleExclusion {
		rec := httptest.NewRecorder()
		a.handleConvertExclusions(rec, httptest.NewRequest(http.MethodGet, "/api/v1/convert/exclusions", nil))
		var out struct {
			Items []convert.TitleExclusion `json:"items"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("list: %d %s", rec.Code, rec.Body.String())
		}
		return out.Items
	}

	if got := list(); len(got) != 0 {
		t.Fatalf("a fresh install lists %+v", got)
	}
	if rec := put(`{"kind":"movie","movie_id":1,"excluded":true}`); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"excluded":true`) {
		t.Fatalf("exclude: %d %s", rec.Code, rec.Body.String())
	}
	if got := list(); len(got) != 1 || got[0].MovieID != 1 || got[0].Title != "Aliens" || got[0].Season != -1 {
		t.Fatalf("listed %+v", got)
	}
	if rec := put(`{"kind":"movie","movie_id":1,"excluded":false}`); rec.Code != http.StatusOK {
		t.Fatalf("allow again: %d %s", rec.Code, rec.Body.String())
	}
	if got := list(); len(got) != 0 {
		t.Fatalf("still listed after turning it off: %+v", got)
	}
	if rec := put(`{"kind":"movie","movie_id":42,"excluded":true}`); rec.Code != http.StatusNotFound {
		t.Errorf("a film that isn't in the library: %d, want 404", rec.Code)
	}
	if rec := put(`{"kind":"series","excluded":true}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no id: %d, want 400", rec.Code)
	}

	rec := httptest.NewRecorder()
	a.handleConvertSettingsUpdate(rec, httptest.NewRequest(http.MethodPut, "/api/v1/convert/settings", strings.NewReader(`{"skip_resolutions":["4K","SD"]}`)))
	var s convert.Settings
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &s) != nil || strings.Join(s.SkipResolutions, ",") != "SD,2160p" {
		t.Fatalf("settings: %d %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	a.handleConvertSettingsUpdate(rec, httptest.NewRequest(http.MethodPut, "/api/v1/convert/settings", strings.NewReader(`{"skip_resolutions":["8K"]}`)))
	if rec.Code == http.StatusOK {
		t.Errorf("an unknown resolution was saved: %s", rec.Body.String())
	}
}
