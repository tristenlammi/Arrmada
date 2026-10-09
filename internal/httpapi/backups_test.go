package httpapi

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/backup"
)

func newBackupServer(t *testing.T) *routeServer {
	t.Helper()
	return newRouteServer(t, func(d *Deps) {
		d.Backups = backup.New(d.Store, d.Settings, d.Log)
	})
}

// doBody is do with a JSON body.
func (s *routeServer) doBody(method, path string, c *http.Cookie, body string) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://arrmada.local"+path, strings.NewReader(body))
	r.RemoteAddr = "192.168.1.20:5000"
	r.Header.Set("Content-Type", "application/json")
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	return rec
}

// manualBackup takes a backup through the API and returns its name.
func manualBackup(t *testing.T, s *routeServer, admin *http.Cookie) string {
	t.Helper()
	rec := s.do("POST", "/api/v1/system/backups", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("back up now: HTTP %d: %s", rec.Code, rec.Body)
	}
	var b backup.Backup
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b.Name
}

// "Back up now" is admin only — a backup holds API keys and password hashes — and an
// admin's request leaves a manual backup in the store's backups folder.
func TestBackupNowAdminOnly(t *testing.T) {
	s := newBackupServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	_, req := s.user(t, "kid@example.com", auth.RoleRequester)

	for name, c := range map[string]*http.Cookie{"manager": mgr, "requester": req} {
		if rec := s.do("POST", "/api/v1/system/backups", c); rec.Code != http.StatusForbidden {
			t.Errorf("%s: HTTP %d, want 403", name, rec.Code)
		}
	}
	rec := s.do("POST", "/api/v1/system/backups", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("admin: HTTP %d: %s", rec.Code, rec.Body)
	}
	var b backup.Backup
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	if b.Kind != "manual" || b.SizeBytes == 0 {
		t.Errorf("backup = %+v", b)
	}
	if _, err := os.Stat(filepath.Join(s.deps.Backups.Dir(), b.Name)); err != nil {
		t.Errorf("the backup file isn't there: %v", err)
	}
}

// Every backups route answers 403 to a manager and a requester, and works for an admin.
func TestBackupRoutesAdminOnly(t *testing.T) {
	s := newBackupServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	_, req := s.user(t, "kid@example.com", auth.RoleRequester)
	name := manualBackup(t, s, admin)

	routes := []struct{ method, path, body string }{
		{"GET", "/api/v1/system/backups", ""},
		{"POST", "/api/v1/system/backups", ""},
		{"PUT", "/api/v1/system/backups/settings", `{"hour":3}`},
		{"GET", "/api/v1/system/backups/" + name + "/download", ""},
		{"DELETE", "/api/v1/system/backups/" + name, ""},
	}
	for _, rt := range routes {
		for who, c := range map[string]*http.Cookie{"manager": mgr, "requester": req} {
			if rec := s.doBody(rt.method, rt.path, c, rt.body); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s as %s: HTTP %d, want 403", rt.method, rt.path, who, rec.Code)
			}
		}
	}
	for _, rt := range routes {
		if rec := s.doBody(rt.method, rt.path, admin, rt.body); rec.Code != http.StatusOK {
			t.Errorf("%s %s as admin: HTTP %d: %s", rt.method, rt.path, rec.Code, rec.Body)
		}
	}
}

// Anything that isn't exactly a backup's file name is refused before it's looked up, so
// neither download nor delete can reach the live database or leave the backups folder.
func TestBackupNameValidation(t *testing.T) {
	s := newBackupServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	manualBackup(t, s, admin)

	bad := []string{
		"..%2Farrmada.db",
		"%2Fdata%2Farrmada.db",
		"arrmada.db",
		"notes.txt",
		"arrmada-weird-20261009T000000Z.db",
		"arrmada-manual-20261009T000000Z.db.part",
		"..%2Fbackups%2Farrmada-manual-20261009T000000Z.db",
	}
	for _, n := range bad {
		if rec := s.do("GET", "/api/v1/system/backups/"+n+"/download", admin); rec.Code != http.StatusBadRequest {
			t.Errorf("download %q: HTTP %d, want 400", n, rec.Code)
		}
		if rec := s.do("DELETE", "/api/v1/system/backups/"+n, admin); rec.Code != http.StatusBadRequest {
			t.Errorf("delete %q: HTTP %d, want 400", n, rec.Code)
		}
	}
	// A well-formed name that isn't there is a 404, not a 400.
	if rec := s.do("GET", "/api/v1/system/backups/arrmada-manual-20200101T000000Z.db/download", admin); rec.Code != http.StatusNotFound {
		t.Errorf("missing backup: HTTP %d, want 404", rec.Code)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(s.deps.Backups.Dir()), "arrmada.db")); err != nil {
		t.Errorf("the live database is gone: %v", err)
	}
}

// A download is a gzip of the backup, marked never to be cached, and decompresses to a
// SQLite database.
func TestBackupDownloadIsGzip(t *testing.T) {
	s := newBackupServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	name := manualBackup(t, s, admin)

	rec := s.do("GET", "/api/v1/system/backups/"+name+"/download", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d: %s", rec.Code, rec.Body)
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/gzip" {
		t.Errorf("Content-Type = %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); cd != `attachment; filename="`+name+`.gz"` {
		t.Errorf("Content-Disposition = %q", cd)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "no-store") {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
	body := rec.Body.Bytes()
	if len(body) < 2 || body[0] != 0x1f || body[1] != 0x8b {
		t.Fatalf("not gzip: % x", body[:min(len(body), 4)])
	}
	zr, err := gzip.NewReader(bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	db, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(db, []byte("SQLite format 3\x00")) {
		t.Errorf("decompressed file isn't SQLite: % x", db[:min(len(db), 16)])
	}
	orig, _ := os.ReadFile(filepath.Join(s.deps.Backups.Dir(), name))
	if !bytes.Equal(db, orig) {
		t.Error("the download doesn't match the backup on disk")
	}
}

// The list carries the backups, their total and the schedule; schedule changes save and
// out-of-range values are refused; delete removes the file.
func TestBackupListSettingsDelete(t *testing.T) {
	s := newBackupServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	name := manualBackup(t, s, admin)

	rec := s.do("GET", "/api/v1/system/backups", admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: HTTP %d: %s", rec.Code, rec.Body)
	}
	var list struct {
		Backups    []backup.Backup `json:"backups"`
		TotalBytes int64           `json:"total_bytes"`
		Settings   backup.Schedule `json:"settings"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Backups) != 1 || list.Backups[0].Name != name || list.TotalBytes != list.Backups[0].SizeBytes {
		t.Errorf("list = %+v", list)
	}
	if list.Backups[0].SchemaVersion == "" {
		t.Error("no schema version on the listed backup")
	}
	if want := (backup.Schedule{Enabled: true, Hour: 4, KeepNightly: 7}); list.Settings != want {
		t.Errorf("default schedule = %+v, want %+v", list.Settings, want)
	}

	rec = s.doBody("PUT", "/api/v1/system/backups/settings", admin, `{"enabled":false,"hour":2,"keep_nightly":14}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings: HTTP %d: %s", rec.Code, rec.Body)
	}
	if got := s.deps.Backups.Schedule(t.Context()); got != (backup.Schedule{Enabled: false, Hour: 2, KeepNightly: 14}) {
		t.Errorf("saved schedule = %+v", got)
	}
	for _, body := range []string{`{"hour":24}`, `{"hour":-1}`, `{"keep_nightly":0}`, `{"keep_nightly":366}`, `{"other":1}`} {
		if rec := s.doBody("PUT", "/api/v1/system/backups/settings", admin, body); rec.Code != http.StatusBadRequest {
			t.Errorf("settings %s: HTTP %d, want 400", body, rec.Code)
		}
	}

	if rec := s.do("DELETE", "/api/v1/system/backups/"+name, admin); rec.Code != http.StatusOK {
		t.Fatalf("delete: HTTP %d: %s", rec.Code, rec.Body)
	}
	if _, err := os.Stat(filepath.Join(s.deps.Backups.Dir(), name)); !os.IsNotExist(err) {
		t.Errorf("the backup is still there: %v", err)
	}
	if rec := s.do("DELETE", "/api/v1/system/backups/"+name, admin); rec.Code != http.StatusNotFound {
		t.Errorf("second delete: HTTP %d, want 404", rec.Code)
	}
}
