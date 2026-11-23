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

// Bulk unblock takes the ticked ids, or everything matching the list's filter up to the
// newest entry the list showed; it's staff-only like the single unblock.
func TestBlocklistBulkUnblock(t *testing.T) {
	s := newRouteServer(t, func(d *Deps) {
		mv := movies.NewService(d.Store.DB(), nil, nil, t.TempDir(), "", nil, d.Log)
		d.Movies = mv
		d.Automation = automation.New(mv, nil, nil, nil, d.Store.DB(), nil, d.Log, "")
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	_, req := s.user(t, "kid@example.com", auth.RoleRequester)
	db := s.st.DB()
	add := func(title, kind string) int64 {
		t.Helper()
		res, err := db.Exec(`INSERT INTO blocklist (movie_id, norm_title, title, media_type) VALUES (0, ?, ?, ?)`, title, title, kind)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}
	count := func() int {
		var n int
		_ = db.QueryRow(`SELECT COUNT(*) FROM blocklist`).Scan(&n)
		return n
	}
	removed := func(rec interface{ Bytes() []byte }) int {
		var out struct {
			Removed int `json:"removed"`
		}
		_ = json.Unmarshal(rec.Bytes(), &out)
		return out.Removed
	}
	a, b := add("Stalled.A", "global"), add("Stalled.B", "global")
	for i := range 3 {
		add(fmt.Sprintf("Stalled.Global.%d", i), "global")
	}
	add("Stalled.Book", "book")
	add("Keep.Me", "global")

	body := fmt.Sprintf(`{"ids":[%d,%d]}`, a, b)
	if rec := s.doJSON("POST", "/api/v1/blocklist/bulk-unblock", req, body); rec.Code != http.StatusForbidden {
		t.Errorf("requester bulk unblock: HTTP %d, want 403", rec.Code)
	}
	for _, bad := range []string{`{}`, `{"ids":[]}`, `{"all":true,"type":"global"}`, `{"all":true,"type":"bogus","through_id":9}`, fmt.Sprintf(`{"all":true,"through_id":9,"ids":[%d]}`, a)} {
		if rec := s.doJSON("POST", "/api/v1/blocklist/bulk-unblock", mgr, bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400", bad, rec.Code)
		}
	}
	if count() != 7 {
		t.Fatalf("a refused request removed something: %d left", count())
	}

	rec := s.doJSON("POST", "/api/v1/blocklist/bulk-unblock", mgr, body)
	if rec.Code != http.StatusOK || removed(rec.Body) != 2 || count() != 5 {
		t.Fatalf("by ids: HTTP %d %s, %d left", rec.Code, rec.Body, count())
	}

	// Everything matching type=global, q=stalled: the list's newest_id bounds it.
	rec = s.do("GET", "/api/v1/blocklist?type=global&q=stalled&limit=1", mgr)
	var list struct {
		Total    int   `json:"total"`
		NewestID int64 `json:"newest_id"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &list)
	if list.Total != 3 || list.NewestID == 0 {
		t.Fatalf("filtered list = %+v", list)
	}
	late := add("Stalled.Late", "global")
	rec = s.doJSON("POST", "/api/v1/blocklist/bulk-unblock", mgr,
		fmt.Sprintf(`{"all":true,"type":"global","q":"stalled","through_id":%d}`, list.NewestID))
	if rec.Code != http.StatusOK || removed(rec.Body) != 3 {
		t.Fatalf("all matching: HTTP %d %s", rec.Code, rec.Body)
	}
	var titles []string
	rows, _ := db.Query(`SELECT title FROM blocklist ORDER BY id`)
	for rows.Next() {
		var s string
		_ = rows.Scan(&s)
		titles = append(titles, s)
	}
	rows.Close()
	if fmt.Sprint(titles) != "[Stalled.Book Keep.Me Stalled.Late]" {
		t.Errorf("left %v (late id %d), want the book, the unmatched one and the one blocked after the list", titles, late)
	}
}
