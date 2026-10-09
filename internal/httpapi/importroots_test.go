package httpapi

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/config"
)

// importTestDirs lays out downloads, a library and Arrmada's data folder side by side in
// a temp dir — never anything real.
func importTestDirs(t *testing.T) (cfg config.Config, outside string) {
	t.Helper()
	base := t.TempDir()
	cfg = config.Config{
		DownloadsDir: filepath.Join(base, "downloads"),
		LibraryDir:   filepath.Join(base, "managed-volume"),
		MoviesDir:    filepath.Join(base, "library", "movies"),
		DataDir:      filepath.Join(base, "data"),
	}
	outside = filepath.Join(base, "elsewhere")
	for _, d := range []string{cfg.DownloadsDir, filepath.Join(cfg.DownloadsDir, "sub"), cfg.LibraryDir, cfg.MoviesDir, cfg.DataDir, outside} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return cfg, outside
}

func TestCheckImportPath(t *testing.T) {
	cfg, outside := importTestDirs(t)
	a := &api{deps: Deps{Config: cfg}}
	ctx := context.Background()

	ok := []string{
		"", // the downloads default
		cfg.DownloadsDir,
		filepath.Join(cfg.DownloadsDir, "sub"),
		filepath.Join(cfg.DownloadsDir, "sub", "Film.2020.mkv"),
		filepath.Join(cfg.MoviesDir, "Film (2020)"),
	}
	for _, p := range ok {
		got, err := a.checkImportPath(ctx, p)
		if err != nil {
			t.Errorf("%q refused: %v", p, err)
			continue
		}
		if p == "" && got != filepath.Clean(cfg.DownloadsDir) {
			t.Errorf("empty path became %q, want the downloads folder", got)
		}
	}

	refused := map[string]error{
		filepath.Join(cfg.DataDir, "arrmada.db"): errImportPathDataDir,
		cfg.DataDir:                              errImportPathDataDir,
		outside:                                  errImportPathOutside,
		"/":                                      errImportPathOutside,
		cfg.DownloadsDir + string(filepath.Separator) + ".." + "/..": errImportPathOutside,
		cfg.DownloadsDir + "-old":                                    errImportPathOutside,
		// ARRMADA_LIBRARY_DIR is the managed volume, not a library anyone picked.
		cfg.LibraryDir: errImportPathOutside,
	}
	for p, want := range refused {
		if _, err := a.checkImportPath(ctx, p); !errors.Is(err, want) {
			t.Errorf("%q: got %v, want %v", p, err, want)
		}
	}

	// A symlink inside downloads that leads out of it is refused.
	link := filepath.Join(cfg.DownloadsDir, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Skipf("symlinks not permitted here: %v", err)
	}
	if _, err := a.checkImportPath(ctx, link); !errors.Is(err, errImportPathOutside) {
		t.Errorf("symlink out of downloads: got %v, want refused", err)
	}
}

// lanRequest builds a request that looks like it came from the home network, signed in
// as role (nil = anonymous).
func lanRequest(method, target, body string, role auth.Role) *http.Request {
	var rd io.Reader
	if body != "" {
		rd = strings.NewReader(body)
	}
	r := httptest.NewRequest(method, target, rd)
	r.RemoteAddr = "192.168.1.20:5000"
	if role != "" {
		r = withUser(r, &auth.User{ID: 7, Username: "someone", Role: role})
	}
	return r
}

func TestManualImportListRequiresManager(t *testing.T) {
	cfg, _ := importTestDirs(t)
	srv := New(Deps{Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for _, kind := range []string{"movies", "series", "books"} {
		// ?path=/ makes the manager's call stop at the folder check (400), so no catalog
		// service is needed — and proves the check runs.
		target := "/api/v1/" + kind + "/1/manualimport?path=" + url.QueryEscape("/")
		for role, want := range map[auth.Role]int{
			auth.RoleReadonly:  http.StatusForbidden,
			auth.RoleRequester: http.StatusForbidden,
			auth.RoleManager:   http.StatusBadRequest,
		} {
			rec := httptest.NewRecorder()
			srv.Handler.ServeHTTP(rec, lanRequest("GET", target, "", role))
			if rec.Code != want {
				t.Errorf("%s as %s: HTTP %d, want %d", target, role, rec.Code, want)
			}
		}
	}
}

func TestManualImportRefusesPathsOutsideTheRoots(t *testing.T) {
	cfg, outside := importTestDirs(t)
	srv := New(Deps{Config: cfg, Log: slog.New(slog.NewTextHandler(io.Discard, nil))})
	for _, kind := range []string{"movies", "series", "books"} {
		for _, p := range []string{outside, cfg.DataDir, "/"} {
			// Listing: the walk never starts.
			rec := httptest.NewRecorder()
			srv.Handler.ServeHTTP(rec, lanRequest("GET", "/api/v1/"+kind+"/1/manualimport?path="+url.QueryEscape(p), "", auth.RoleManager))
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "folder") {
				t.Errorf("GET %s ?path=%s: HTTP %d %s", kind, p, rec.Code, rec.Body.String())
			}
			// Importing: refused before any catalog service is touched (none is wired
			// here, so reaching one would panic into a 500).
			rec = httptest.NewRecorder()
			body := `{"path":` + jsonString(filepath.Join(p, "Film.mkv")) + `}`
			srv.Handler.ServeHTTP(rec, lanRequest("POST", "/api/v1/"+kind+"/1/manualimport", body, auth.RoleManager))
			if rec.Code != http.StatusBadRequest {
				t.Errorf("POST %s from %s: HTTP %d %s", kind, p, rec.Code, rec.Body.String())
			}
		}
	}
}

// A listing that ran out of time answers with what it found, marked cut short; one whose
// browser has gone answers nothing.
func TestWriteImportListTimeoutAndDisconnect(t *testing.T) {
	a := &api{deps: Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}

	r := httptest.NewRequest("GET", "/", nil)
	ctx, cancel := context.WithTimeout(r.Context(), 0)
	defer cancel()
	<-ctx.Done()
	rec := httptest.NewRecorder()
	a.writeImportList(rec, r, ctx, "movies", "/dl", []string{"a"}, false)
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), `"truncated":true`) || !strings.Contains(rec.Body.String(), `"note"`) {
		t.Errorf("timed-out listing: %d %s", rec.Code, rec.Body.String())
	}

	gone, leave := context.WithCancel(context.Background())
	leave()
	r = httptest.NewRequest("GET", "/", nil).WithContext(gone)
	rec = httptest.NewRecorder()
	a.writeImportList(rec, r, gone, "movies", "/dl", []string{}, false)
	if rec.Body.Len() != 0 {
		t.Errorf("answered a browser that had gone: %s", rec.Body.String())
	}

	rec = httptest.NewRecorder()
	ok := httptest.NewRequest("GET", "/", nil)
	a.writeImportList(rec, ok, ok.Context(), "movies", "/dl", []string{"a"}, true)
	if !strings.Contains(rec.Body.String(), `"truncated":true`) || strings.Contains(rec.Body.String(), `"note"`) {
		t.Errorf("capped listing: %s", rec.Body.String())
	}
}

func jsonString(s string) string {
	return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
}
