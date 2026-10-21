package store_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/backup"
	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/httpapi"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

func TestPostgresApplicationBackupRoutes(t *testing.T) {
	ctx, _, opt, _ := store.PostgresFixture(t)
	opt.BinDir = os.Getenv("ARRMADA_TEST_POSTGRES_BIN")
	st, err := store.OpenWith(t.TempDir(), store.Options{Backend: store.Postgres, Postgres: opt})
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	set, err := settings.Open(ctx, st.DB())
	if err != nil {
		t.Fatal(err)
	}
	svc := backup.New(st, set, nil)
	accounts := auth.NewService(st.DB())
	cookies := map[auth.Role]*http.Cookie{}
	for _, role := range []auth.Role{auth.RoleAdmin, auth.RoleRequester} {
		user, err := accounts.CreateUser(ctx, string(role), "test-password", role, false)
		if err != nil {
			t.Fatal(err)
		}
		token, _, err := accounts.CreateSession(ctx, user.ID)
		if err != nil {
			t.Fatal(err)
		}
		cookies[role] = &http.Cookie{Name: "arrmada_session", Value: token}
	}
	handler := httpapi.New(httpapi.Deps{Config: config.Config{DataDir: st.DataDir(), DatabaseBackend: "postgres"}, Store: st, Settings: set, Backups: svc, Auth: accounts, Log: slog.New(slog.NewTextHandler(io.Discard, nil))}).Handler
	do := func(method, path, body string, role auth.Role) *httptest.ResponseRecorder {
		r := httptest.NewRequest(method, "http://arrmada.local"+path, strings.NewReader(body))
		r.RemoteAddr = "192.168.1.20:5000"
		r.Header.Set("Content-Type", "application/json")
		if cookie := cookies[role]; cookie != nil {
			r.AddCookie(cookie)
		}
		w := httptest.NewRecorder()
		handler.ServeHTTP(w, r)
		return w
	}
	if w := do("POST", "/api/v1/system/backups", "", auth.RoleRequester); w.Code != http.StatusForbidden {
		t.Fatalf("requester could access database backups: %d", w.Code)
	}
	w := do("POST", "/api/v1/system/backups", "", auth.RoleAdmin)
	if w.Code != http.StatusOK {
		t.Fatalf("backup route: %d %s", w.Code, w.Body)
	}
	var file backup.Backup
	if err := json.Unmarshal(w.Body.Bytes(), &file); err != nil {
		t.Fatal(err)
	}
	w = do("GET", "/api/v1/system/backups", "", auth.RoleAdmin)
	var list struct {
		Backend    string          `json:"backend"`
		CanRestore bool            `json:"can_restore"`
		CanUpload  bool            `json:"can_upload"`
		Backups    []backup.Backup `json:"backups"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil || w.Code != http.StatusOK || list.Backend != "postgres" || list.CanRestore || list.CanUpload || len(list.Backups) != 1 {
		t.Fatalf("backup capabilities: %d %+v %v", w.Code, list, err)
	}
	w = do("GET", "/api/v1/system/backups/"+file.Name+"/download", "", auth.RoleAdmin)
	if w.Code != http.StatusOK || !strings.HasPrefix(w.Body.String(), "PGDMP") || w.Header().Get("Content-Type") != "application/octet-stream" || strings.Contains(w.Header().Get("Content-Disposition"), ".gz") || !strings.Contains(w.Header().Get("Cache-Control"), "no-store") {
		t.Fatalf("native download route: %d %v", w.Code, w.Header())
	}
	for _, route := range []struct{ method, path string }{
		{"POST", "/api/v1/system/backups/" + file.Name + "/restore"},
		{"POST", "/api/v1/system/backups/upload"},
		{"DELETE", "/api/v1/system/backups/restore-pending"},
	} {
		w := do(route.method, route.path, `{"confirm":"RESTORE"}`, auth.RoleAdmin)
		if w.Code != http.StatusConflict {
			t.Fatalf("unsafe PostgreSQL restore route allowed: %s %d %s", route.path, w.Code, w.Body)
		}
	}
}
