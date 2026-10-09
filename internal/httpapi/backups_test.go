package httpapi

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"mime/multipart"
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

// doJSON is do with a JSON body (cookie before body).
func (s *routeServer) doJSON(method, path string, c *http.Cookie, body string) *httptest.ResponseRecorder {
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
		{"POST", "/api/v1/system/backups/" + name + "/restore", `{"confirm":"RESTORE"}`},
		{"DELETE", "/api/v1/system/backups/restore-pending", ""},
		{"DELETE", "/api/v1/system/backups/" + name, ""},
	}
	for _, rt := range routes {
		for who, c := range map[string]*http.Cookie{"manager": mgr, "requester": req} {
			if rec := s.doJSON(rt.method, rt.path, c, rt.body); rec.Code != http.StatusForbidden {
				t.Errorf("%s %s as %s: HTTP %d, want 403", rt.method, rt.path, who, rec.Code)
			}
		}
	}
	for _, rt := range routes {
		if rec := s.doJSON(rt.method, rt.path, admin, rt.body); rec.Code != http.StatusOK {
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

	rec = s.doJSON("PUT", "/api/v1/system/backups/settings", admin, `{"enabled":false,"hour":2,"keep_nightly":14}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("settings: HTTP %d: %s", rec.Code, rec.Body)
	}
	if got := s.deps.Backups.Schedule(t.Context()); got != (backup.Schedule{Enabled: false, Hour: 2, KeepNightly: 14}) {
		t.Errorf("saved schedule = %+v", got)
	}
	for _, body := range []string{`{"hour":24}`, `{"hour":-1}`, `{"keep_nightly":0}`, `{"keep_nightly":366}`, `{"other":1}`} {
		if rec := s.doJSON("PUT", "/api/v1/system/backups/settings", admin, body); rec.Code != http.StatusBadRequest {
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

// Restore needs the typed phrase, stages the backup for the next start (shown in the
// list), protects it from Delete, and can be cancelled. Without self-restart the answer
// carries the manual command instead.
func TestBackupRestoreStagesAndCancels(t *testing.T) {
	s := newBackupServer(t) // Restart stays nil: the app can't restart itself
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	name := manualBackup(t, s, admin)
	path := "/api/v1/system/backups/" + name + "/restore"

	for _, body := range []string{`{}`, `{"confirm":"restore"}`, `{"confirm":"yes"}`} {
		if rec := s.doJSON("POST", path, admin, body); rec.Code != http.StatusBadRequest {
			t.Errorf("restore with %s: HTTP %d, want 400", body, rec.Code)
		}
	}
	if m, _ := s.deps.Backups.PendingRestore(); m != nil {
		t.Fatal("staged without the typed phrase")
	}

	rec := s.doJSON("POST", path, admin, `{"confirm":"RESTORE"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("restore: HTTP %d: %s", rec.Code, rec.Body)
	}
	var out struct {
		Staged        bool   `json:"staged"`
		Restarting    bool   `json:"restarting"`
		ManualCommand string `json:"manual_command"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if !out.Staged || out.Restarting || out.ManualCommand == "" {
		t.Errorf("restore answer = %+v", out)
	}

	rec = s.do("GET", "/api/v1/system/backups", admin)
	var list struct {
		Pending *struct {
			Name        string `json:"name"`
			RequestedBy string `json:"requested_by"`
		} `json:"pending_restore"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if list.Pending == nil || list.Pending.Name != name || list.Pending.RequestedBy != "admin@example.com" {
		t.Errorf("pending_restore = %+v", list.Pending)
	}

	if rec := s.do("DELETE", "/api/v1/system/backups/"+name, admin); rec.Code != http.StatusConflict {
		t.Errorf("deleting the staged backup: HTTP %d, want 409", rec.Code)
	}
	rec = s.do("DELETE", "/api/v1/system/backups/restore-pending", admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"cancelled":true`) {
		t.Errorf("cancel: HTTP %d: %s", rec.Code, rec.Body)
	}
	if m, _ := s.deps.Backups.PendingRestore(); m != nil {
		t.Error("still staged after cancel")
	}
}

// Inside Docker the app restarts itself once the restore is staged.
func TestBackupRestoreRestartsInContainer(t *testing.T) {
	restarted := false
	s := newRouteServer(t, func(d *Deps) {
		d.Backups = backup.New(d.Store, d.Settings, d.Log)
		d.Restart = func() { restarted = true }
	})
	t.Setenv("ARRMADA_IN_CONTAINER", "1")
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	name := manualBackup(t, s, admin)
	rec := s.doJSON("POST", "/api/v1/system/backups/"+name+"/restore", admin, `{"confirm":"RESTORE"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"restarting":true`) || !restarted {
		t.Errorf("HTTP %d: %s (restarted %v)", rec.Code, rec.Body, restarted)
	}
}

// A damaged backup is refused with a reason and nothing is staged.
func TestBackupRestoreRefusesDamaged(t *testing.T) {
	s := newBackupServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	name := manualBackup(t, s, admin)
	if err := os.WriteFile(filepath.Join(s.deps.Backups.Dir(), name), []byte("garbage, not sqlite"), 0o644); err != nil {
		t.Fatal(err)
	}
	rec := s.doJSON("POST", "/api/v1/system/backups/"+name+"/restore", admin, `{"confirm":"RESTORE"}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "not a SQLite database") {
		t.Errorf("HTTP %d: %s", rec.Code, rec.Body)
	}
	if m, _ := s.deps.Backups.PendingRestore(); m != nil {
		t.Error("a damaged backup was staged")
	}
	if rec := s.doJSON("POST", "/api/v1/system/backups/..%2Farrmada.db/restore", admin, `{"confirm":"RESTORE"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("bad name: HTTP %d, want 400", rec.Code)
	}
}

// upload posts data as the multipart "file" field.
func (s *routeServer) upload(t *testing.T, c *http.Cookie, data []byte) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, err := mw.CreateFormFile("file", "backup.db.gz")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(data)
	_ = mw.Close()
	r := httptest.NewRequest("POST", "http://arrmada.local/api/v1/system/backups/upload", &body)
	r.RemoteAddr = "192.168.1.20:5000"
	r.Header.Set("Content-Type", mw.FormDataContentType())
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	return rec
}

func gzipped(t *testing.T, b []byte) []byte {
	t.Helper()
	var out bytes.Buffer
	zw := gzip.NewWriter(&out)
	_, _ = zw.Write(b)
	_ = zw.Close()
	return out.Bytes()
}

// leftovers lists anything in the backups folder that isn't a listed backup (a .part).
func leftovers(t *testing.T, dir string) []string {
	t.Helper()
	entries, _ := os.ReadDir(dir)
	var out []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".part") || strings.HasSuffix(e.Name(), ".tmp") {
			out = append(out, e.Name())
		}
	}
	return out
}

// A plain .db and the .db.gz Download makes are both taken in as Uploaded backups, and
// an uploaded backup restores through the normal flow.
func TestUploadAcceptsGzipAndPlain(t *testing.T) {
	s := newBackupServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	name := manualBackup(t, s, admin)
	db, err := os.ReadFile(filepath.Join(s.deps.Backups.Dir(), name))
	if err != nil {
		t.Fatal(err)
	}
	if rec := s.upload(t, mgr, db); rec.Code != http.StatusForbidden {
		t.Errorf("manager upload: HTTP %d, want 403", rec.Code)
	}

	var names []string
	for label, data := range map[string][]byte{"plain": db, "gzip": gzipped(t, db)} {
		rec := s.upload(t, admin, data)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: HTTP %d: %s", label, rec.Code, rec.Body)
		}
		var b backup.Backup
		_ = json.Unmarshal(rec.Body.Bytes(), &b)
		if b.Kind != "uploaded" || b.SchemaVersion == "" {
			t.Errorf("%s: uploaded = %+v", label, b)
		}
		got, _ := os.ReadFile(filepath.Join(s.deps.Backups.Dir(), b.Name))
		if !bytes.Equal(got, db) {
			t.Errorf("%s: the stored file differs from what was uploaded", label)
		}
		names = append(names, b.Name)
	}
	if names[0] == names[1] {
		t.Errorf("two uploads in the same second share a name: %v", names)
	}
	if rec := s.doJSON("POST", "/api/v1/system/backups/"+names[1]+"/restore", admin, `{"confirm":"RESTORE"}`); rec.Code != http.StatusOK {
		t.Errorf("restoring the upload: HTTP %d: %s", rec.Code, rec.Body)
	}
	if l := leftovers(t, s.deps.Backups.Dir()); len(l) > 0 {
		t.Errorf("left behind: %v", l)
	}
}

// Anything that isn't a sound Arrmada database is refused with a reason, and leaves
// nothing behind.
func TestUploadRejectsBadMagicAndCorrupt(t *testing.T) {
	s := newBackupServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	junk := bytes.Repeat([]byte("not a database "), 500)
	header := append([]byte("SQLite format 3\x00"), junk...)
	cases := map[string][]byte{
		"text":             junk,
		"empty":            nil,
		"gzipped text":     gzipped(t, junk),
		"header + garbage": header,
		"gzipped garbage":  gzipped(t, header),
		"truncated gzip":   gzipped(t, header)[:20],
	}
	for label, data := range cases {
		rec := s.upload(t, admin, data)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400: %s", label, rec.Code, rec.Body)
		}
	}
	list, _ := s.deps.Backups.List(t.Context())
	if len(list) != 0 {
		t.Errorf("refused uploads were listed: %+v", list)
	}
	if l := leftovers(t, s.deps.Backups.Dir()); len(l) > 0 {
		t.Errorf("left behind: %v", l)
	}
}

// Over the size limit is a 413, whether the client says so up front or not.
func TestUploadTooLarge(t *testing.T) {
	old := maxBackupUpload
	maxBackupUpload = 4096
	t.Cleanup(func() { maxBackupUpload = old })
	s := newBackupServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	big := append([]byte("SQLite format 3\x00"), make([]byte, 64<<10)...)

	if rec := s.upload(t, admin, big); rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("declared length: HTTP %d, want 413: %s", rec.Code, rec.Body)
	}

	// Chunked: no Content-Length, so the cap is only found while reading.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "backup.db")
	_, _ = fw.Write(big)
	_ = mw.Close()
	r := httptest.NewRequest("POST", "http://arrmada.local/api/v1/system/backups/upload", io.MultiReader(&body))
	r.ContentLength = -1
	r.RemoteAddr = "192.168.1.20:5000"
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.AddCookie(admin)
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("streamed: HTTP %d, want 413: %s", rec.Code, rec.Body)
	}
	if l := leftovers(t, s.deps.Backups.Dir()); len(l) > 0 {
		t.Errorf("left behind: %v", l)
	}
}
