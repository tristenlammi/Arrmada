package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// The Blocklist lists every kind of entry and unblocks any of them; it's staff-only.
func TestBlocklistListAndUnblock(t *testing.T) {
	s := newRouteServer(t, func(d *Deps) {
		mv := movies.NewService(d.Store.DB(), nil, nil, t.TempDir(), "", nil, d.Log)
		d.Movies = mv
		d.Automation = automation.New(mv, nil, nil, nil, d.Store.DB(), nil, d.Log, "")
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	_, req := s.user(t, "kid@example.com", auth.RoleRequester)
	db := s.st.DB()
	res, err := db.Exec(`INSERT INTO blocklist (movie_id, norm_title, title, reason, media_type) VALUES (0, 'fake', 'Fake.2024.exe', 'rejected in review', 'global')`)
	if err != nil {
		t.Fatal(err)
	}
	gid, _ := res.LastInsertId()
	if _, err := db.Exec(`INSERT INTO blocklist (movie_id, norm_title, title, media_type) VALUES (3, 'dune', 'Dune [EPUB]', 'book')`); err != nil {
		t.Fatal(err)
	}

	rec := s.do("GET", "/api/v1/blocklist?type=global", mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("list: HTTP %d: %s", rec.Code, rec.Body)
	}
	var list struct {
		Items []automation.BlockRow `json:"items"`
		Total int                   `json:"total"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if list.Total != 1 || len(list.Items) != 1 || list.Items[0].ID != gid {
		t.Fatalf("global list = %+v", list)
	}
	if rec := s.do("GET", "/api/v1/blocklist?type=bogus", mgr); rec.Code != http.StatusBadRequest {
		t.Errorf("bad type: HTTP %d, want 400", rec.Code)
	}

	path := fmt.Sprintf("/api/v1/blocklist/%d", gid)
	if rec := s.do("DELETE", path, req); rec.Code != http.StatusForbidden {
		t.Errorf("requester unblock: HTTP %d, want 403", rec.Code)
	}
	if rec := s.do("GET", "/api/v1/blocklist", req); rec.Code != http.StatusForbidden {
		t.Errorf("requester list: HTTP %d, want 403", rec.Code)
	}
	if rec := s.do("DELETE", path, mgr); rec.Code != http.StatusNoContent {
		t.Fatalf("unblock: HTTP %d: %s", rec.Code, rec.Body)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM blocklist WHERE id = ?`, gid).Scan(&n)
	if n != 0 {
		t.Error("the row is still there")
	}
	if rec := s.do("DELETE", path, mgr); rec.Code != http.StatusNotFound {
		t.Errorf("second unblock: HTTP %d, want 404", rec.Code)
	}
}
