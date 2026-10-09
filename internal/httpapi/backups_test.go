package httpapi

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/backup"
)

// "Back up now" is admin only — a backup holds API keys and password hashes — and an
// admin's request leaves a manual backup in the store's backups folder.
func TestBackupNowAdminOnly(t *testing.T) {
	s := newRouteServer(t, func(d *Deps) {
		d.Backups = backup.New(d.Store, d.Settings, d.Log)
	})
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
