package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/series"
)

// requestsServer is a route server with a real requests service over the store.
func requestsServer(t *testing.T) *routeServer {
	t.Helper()
	root := t.TempDir()
	return newRouteServer(t, func(d *Deps) {
		db := d.Store.DB()
		d.Books = books.NewService(db, nil, d.Log)
		d.Quality = quality.NewService(db)
		mv := movies.NewService(db, nil, nil, root, "", nil, d.Log)
		sr := series.NewService(db, nil, root, d.Log)
		d.Requests = requests.NewService(db, mv, sr, d.Books, nil, d.Quality, nil, "", d.Log)
	})
}

// addRequest stores a request straight in the table.
func addRequest(t *testing.T, s *routeServer, tmdb int, title, status string, by int64, byName string) int64 {
	t.Helper()
	res, err := s.st.DB().Exec(`INSERT INTO requests (media_type, tmdb_id, title, status, requested_by, requested_by_name, note)
		VALUES ('movie', ?, ?, ?, ?, ?, 'please')`, tmdb, title, status, by, byName)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
}

type listBody struct {
	Requests []requests.Request `json:"requests"`
	Counts   requests.Counts    `json:"counts"`
	Total    int                `json:"total"`
}

func getList(t *testing.T, s *routeServer, path string, c *http.Cookie) listBody {
	t.Helper()
	rec := s.do("GET", path, c)
	if rec.Code != http.StatusOK {
		t.Fatalf("%s: HTTP %d: %s", path, rec.Code, rec.Body)
	}
	var b listBody
	if err := json.Unmarshal(rec.Body.Bytes(), &b); err != nil {
		t.Fatal(err)
	}
	return b
}

// Sections filter and order, count every section, and page.
func TestRequestsAPISections(t *testing.T) {
	s := requestsServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	for i := 0; i < 3; i++ {
		addRequest(t, s, 100+i, fmt.Sprintf("Pending %d", i), "pending", 7, "alice")
	}
	addRequest(t, s, 200, "Searching", "approved", 7, "alice")
	ready := addRequest(t, s, 201, "Ready", "approved", 7, "alice")
	if _, err := s.st.DB().Exec(`UPDATE requests SET ready_at = 1700000000 WHERE id = ?`, ready); err != nil {
		t.Fatal(err)
	}
	addRequest(t, s, 202, "Declined", "declined", 7, "alice")
	// The oldest pending first: make the first one older.
	if _, err := s.st.DB().Exec(`UPDATE requests SET created_at = '2020-01-01 00:00:00' WHERE title = 'Pending 2'`); err != nil {
		t.Fatal(err)
	}

	b := getList(t, s, "/api/v1/requests?section=needs_approval&limit=2", admin)
	if b.Total != 3 || len(b.Requests) != 2 || b.Requests[0].Title != "Pending 2" {
		t.Fatalf("needs_approval page = total %d, %d rows, first %q", b.Total, len(b.Requests), b.Requests[0].Title)
	}
	if b.Counts != (requests.Counts{NeedsApproval: 3, InProgress: 1, Ready: 1, Declined: 1}) {
		t.Errorf("counts = %+v", b.Counts)
	}
	b = getList(t, s, "/api/v1/requests?section=needs_approval&limit=2&offset=2", admin)
	if len(b.Requests) != 1 {
		t.Errorf("second page = %d rows, want 1", len(b.Requests))
	}
	for section, want := range map[string]string{"in_progress": "Searching", "ready": "Ready", "declined": "Declined"} {
		b = getList(t, s, "/api/v1/requests?section="+section, admin)
		if len(b.Requests) != 1 || b.Requests[0].Title != want {
			t.Errorf("%s = %+v, want %s", section, b.Requests, want)
		}
	}
	if b = getList(t, s, "/api/v1/requests", admin); len(b.Requests) != 6 {
		t.Errorf("the default list = %d rows, want all 6", len(b.Requests))
	}
	if b = getList(t, s, "/api/v1/requests?section=needs_approval&q=pending%201", admin); len(b.Requests) != 1 {
		t.Errorf("title search = %d rows, want 1", len(b.Requests))
	}
	if rec := s.do("GET", "/api/v1/requests?section=bogus", admin); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown section: HTTP %d, want 400", rec.Code)
	}
}

// A requester sees their own requests and the ones they follow — marked subscriber,
// without the owner's name or note — and never anyone else's, in a list or by id. They
// can stop following; they can't withdraw someone else's request.
func TestRequestsAPIRequesterScope(t *testing.T) {
	s := requestsServer(t)
	alice, aliceCookie := s.user(t, "alice@example.com", auth.RoleRequester)
	bob, bobCookie := s.user(t, "bob@example.com", auth.RoleRequester)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	shared := addRequest(t, s, 1, "Shared", "pending", alice.ID, alice.Username)
	private := addRequest(t, s, 2, "Alice only", "pending", alice.ID, alice.Username)
	own := addRequest(t, s, 3, "Bob's own", "pending", bob.ID, bob.Username)
	if _, err := s.st.DB().Exec(`INSERT INTO request_subscribers (request_id, user_id, user_name) VALUES (?, ?, ?)`, shared, bob.ID, bob.Username); err != nil {
		t.Fatal(err)
	}

	b := getList(t, s, "/api/v1/requests?section=needs_approval", bobCookie)
	rel := map[int64]requests.Request{}
	for _, rq := range b.Requests {
		rel[rq.ID] = rq
	}
	if len(b.Requests) != 2 || rel[own].Relation != "owner" || rel[shared].Relation != "subscriber" {
		t.Fatalf("bob's list = %+v, want his own (owner) and the shared one (subscriber)", b.Requests)
	}
	if rq := rel[shared]; rq.RequestedByName != "" || rq.RequestedBy != 0 || rq.Note != "" {
		t.Errorf("a follower sees who asked: %+v", rq)
	}
	if b.Counts.NeedsApproval != 2 {
		t.Errorf("bob's counts = %+v, want only his own scope", b.Counts)
	}
	if rec := s.do("GET", fmt.Sprintf("/api/v1/requests/%d", private), bobCookie); rec.Code != http.StatusNotFound {
		t.Errorf("someone else's request by id: HTTP %d, want 404", rec.Code)
	}
	if rec := s.do("GET", fmt.Sprintf("/api/v1/requests/%d", shared), bobCookie); rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), alice.Username) {
		t.Errorf("a followed request by id: HTTP %d %s", rec.Code, rec.Body)
	}
	// Staff see who follows it.
	rec := s.do("GET", fmt.Sprintf("/api/v1/requests/%d", shared), admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"followers":[{"name":"`+bob.Username+`"}]`) {
		t.Errorf("staff detail: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.do("DELETE", fmt.Sprintf("/api/v1/requests/%d", shared), bobCookie); rec.Code != http.StatusForbidden {
		t.Errorf("withdrawing someone else's request: HTTP %d, want 403", rec.Code)
	}
	if rec := s.do("DELETE", fmt.Sprintf("/api/v1/requests/%d/subscription", shared), bobCookie); rec.Code != http.StatusNoContent {
		t.Fatalf("stop following: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.do("DELETE", fmt.Sprintf("/api/v1/requests/%d/subscription", shared), bobCookie); rec.Code != http.StatusNotFound {
		t.Errorf("stop following twice: HTTP %d, want 404", rec.Code)
	}
	if rec := s.do("DELETE", fmt.Sprintf("/api/v1/requests/%d/subscription", shared), aliceCookie); rec.Code != http.StatusNotFound {
		t.Errorf("the owner has no subscription to drop: HTTP %d, want 404", rec.Code)
	}
	if b := getList(t, s, "/api/v1/requests?section=needs_approval", bobCookie); len(b.Requests) != 1 {
		t.Errorf("after stopping, bob still lists %d", len(b.Requests))
	}
	if b := getList(t, s, "/api/v1/requests?section=needs_approval", aliceCookie); len(b.Requests) != 2 {
		t.Errorf("alice's own requests changed: %d", len(b.Requests))
	}
}

// A note is trimmed and kept to 500 characters; a longer one is refused, not cut.
func TestRequestsAPICreateRejectsLongNote(t *testing.T) {
	s := requestsServer(t)
	_, cookie := s.user(t, "alice@example.com", auth.RoleRequester)
	long := strings.Repeat("é", 501)
	rec := s.doJSON("POST", "/api/v1/requests", cookie, `{"media_type":"movie","tmdb_id":5,"title":"Up","note":"`+long+`"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("a 501-character note: HTTP %d, want 400", rec.Code)
	}
	rec = s.doJSON("POST", "/api/v1/requests", cookie, `{"media_type":"movie","tmdb_id":5,"title":"Up","note":"  `+strings.Repeat("é", 500)+`  "}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("a 500-character note: HTTP %d %s", rec.Code, rec.Body)
	}
	var got struct {
		Request requests.Request `json:"request"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got.Request.Note != strings.Repeat("é", 500) {
		t.Errorf("note = %q…, want it trimmed and whole", got.Request.Note[:10])
	}
}

// Bulk decisions are staff-only, validated, and reported per request.
func TestRequestsAPIBulk(t *testing.T) {
	s := requestsServer(t)
	_, requester := s.user(t, "alice@example.com", auth.RoleRequester)
	_, manager := s.user(t, "boss@example.com", auth.RoleManager)
	p1 := addRequest(t, s, 1, "One", "pending", 7, "alice")
	p2 := addRequest(t, s, 2, "Two", "pending", 7, "alice")
	body := fmt.Sprintf(`{"action":"decline","ids":[%d,999,%d]}`, p1, p2)
	if rec := s.doJSON("POST", "/api/v1/requests/bulk", requester, body); rec.Code != http.StatusForbidden {
		t.Fatalf("a requester's bulk action: HTTP %d, want 403", rec.Code)
	}
	if rec := s.doJSON("POST", "/api/v1/requests/bulk", manager, `{"action":"delete","ids":[1]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("an unknown action: HTTP %d, want 400", rec.Code)
	}
	if rec := s.doJSON("POST", "/api/v1/requests/bulk", manager, `{"action":"approve","ids":[]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no ids: HTTP %d, want 400", rec.Code)
	}
	rec := s.doJSON("POST", "/api/v1/requests/bulk", manager, body)
	if rec.Code != http.StatusOK {
		t.Fatalf("bulk decline: HTTP %d %s", rec.Code, rec.Body)
	}
	var got struct {
		Results []requests.BulkResult `json:"results"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Results) != 3 || !got.Results[0].OK || got.Results[1].OK || !got.Results[2].OK {
		t.Fatalf("results = %+v", got.Results)
	}
	if b := getList(t, s, "/api/v1/requests?section=declined", manager); b.Total != 2 {
		t.Errorf("declined = %d, want 2", b.Total)
	}
}

// The adult filter is always on, including for a request posted straight to the API: a
// title the filter matches is refused and nothing is stored.
func TestRequestsAPICreateRefusesAdultTitles(t *testing.T) {
	s := requestsServer(t)
	_, cookie := s.user(t, "alice@example.com", auth.RoleRequester)
	rec := s.doJSON("POST", "/api/v1/requests", cookie, `{"media_type":"movie","tmdb_id":77,"title":"A Gangbang Story"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("adult title: HTTP %d %s, want 404", rec.Code, rec.Body)
	}
	rec = s.doJSON("GET", "/api/v1/requests", cookie, "")
	if strings.Contains(rec.Body.String(), "Gangbang") {
		t.Errorf("the refused request was stored: %s", rec.Body)
	}
	rec = s.doJSON("POST", "/api/v1/requests", cookie, `{"media_type":"movie","tmdb_id":78,"title":"Sex Education"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("mainstream title: HTTP %d %s", rec.Code, rec.Body)
	}
}
