package httpapi

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
)

const testHash2 = "89abcdef0123456789abcdef0123456789abcdef"

type bulkRemoveAnswer struct {
	Mode    string              `json:"mode"`
	Removed int                 `json:"removed"`
	Failed  []bulkRemoveFailure `json:"failed"`
}

func bulkRemove(t *testing.T, s *routeServer, c *http.Cookie, body string) (int, bulkRemoveAnswer) {
	t.Helper()
	rec := s.doJSON("POST", "/api/v1/queue/remove", c, body)
	var ans bulkRemoveAnswer
	if rec.Code == http.StatusOK {
		if err := json.Unmarshal(rec.Body.Bytes(), &ans); err != nil {
			t.Fatalf("answer: %v: %s", err, rec.Body)
		}
	}
	return rec.Code, ans
}

// Each picked download is removed exactly as the single Delete removes it, with the one
// files choice for all of them; files are kept unless delete_files is asked for.
func TestBulkRemoveDownloadsModes(t *testing.T) {
	for _, tc := range []struct{ mode, wantDelete string }{
		{"", "false"},
		{"keep_files", "false"},
		{"delete_files", "true"},
	} {
		s, q, mgr := downloadServer(t)
		mode := ""
		if tc.mode != "" {
			mode = `,"mode":"` + tc.mode + `"`
		}
		code, ans := bulkRemove(t, s, mgr, `{"items":[{"hash":"`+testHash+`","name":"a"},{"hash":"`+testHash2+`","name":"b"}]`+mode+`}`)
		if code != http.StatusOK || ans.Removed != 2 || len(ans.Failed) != 0 {
			t.Fatalf("%q: HTTP %d, %+v", tc.mode, code, ans)
		}
		paths, forms := q.snapshot()
		if len(paths) != 2 {
			t.Fatalf("%q: client calls = %v, want two deletes", tc.mode, paths)
		}
		for i, want := range []string{testHash, testHash2} {
			if paths[i] != "/api/v2/torrents/delete" || forms[i].Get("hashes") != want || forms[i].Get("deleteFiles") != tc.wantDelete {
				t.Errorf("%q: call %d = %s %v, want delete of %s deleteFiles=%s", tc.mode, i, paths[i], forms[i], want, tc.wantDelete)
			}
		}
	}
}

// The grab behind a removed download is closed out as removed, as the single Delete does —
// not left 'grabbed' for the stall check to read as vanished and replace.
func TestBulkRemoveClosesGrab(t *testing.T) {
	s, _, mgr := downloadServer(t)
	if _, err := s.st.DB().Exec(`INSERT INTO grabs (movie_id, version_id, title, indexer, media_type, info_hash, status) VALUES (1, 0, 'x', 'x', 'movie', ?, 'grabbed')`, testHash); err != nil {
		t.Fatal(err)
	}
	if code, ans := bulkRemove(t, s, mgr, `{"items":[{"hash":"`+testHash+`","name":"x"}],"mode":"keep_files"}`); code != http.StatusOK || ans.Removed != 1 {
		t.Fatalf("HTTP %d, %+v", code, ans)
	}
	var status string
	if err := s.st.DB().QueryRow(`SELECT status FROM grabs WHERE info_hash = ?`, testHash).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "removed" {
		t.Errorf("grab status = %q, want removed", status)
	}
}

// One download the client refuses is reported and the rest are still removed.
func TestBulkRemoveReportsEachFailure(t *testing.T) {
	s, q, mgr := downloadServer(t)
	q.failHash = testHash
	code, ans := bulkRemove(t, s, mgr, `{"items":[{"hash":"`+testHash+`"},{"hash":"`+testHash2+`"}],"mode":"delete_files"}`)
	if code != http.StatusOK {
		t.Fatalf("HTTP %d", code)
	}
	if ans.Removed != 1 || len(ans.Failed) != 1 || ans.Failed[0].Hash != testHash || ans.Failed[0].Error == "" {
		t.Fatalf("answer = %+v, want one removed and %s failed", ans, testHash)
	}
	if paths, _ := q.snapshot(); len(paths) != 2 {
		t.Errorf("client calls = %v, want both tried", paths)
	}
}

// A bad key anywhere refuses the whole batch before the client is called; block (which
// searches per download) and unknown modes are refused; the same hash twice is removed once.
func TestBulkRemoveRefusals(t *testing.T) {
	s, q, mgr := downloadServer(t)
	for _, body := range []string{
		`{"items":[{"hash":"` + testHash + `"},{"hash":"all"}]}`,
		`{"items":[{"hash":"` + testHash + `|` + testHash2 + `"}]}`,
		`{"items":[{"hash":""}]}`,
		`{"items":[]}`,
		`{"items":[{"hash":"` + testHash + `"}],"mode":"block"}`,
		`{"items":[{"hash":"` + testHash + `"}],"mode":"wipe"}`,
		`{"items":[{"hash":"` + testHash + `"}],"unmonitor":true}`,
	} {
		if code, _ := bulkRemove(t, s, mgr, body); code != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400", body, code)
		}
	}
	tooMany := `{"items":[` + strings.Repeat(`{"hash":"`+testHash+`"},`, maxBulkRemove) + `{"hash":"` + testHash + `"}]}`
	if code, _ := bulkRemove(t, s, mgr, tooMany); code != http.StatusBadRequest {
		t.Errorf("over the cap: HTTP %d, want 400", code)
	}
	if paths, _ := q.snapshot(); len(paths) != 0 {
		t.Fatalf("the client was called: %v", paths)
	}

	code, ans := bulkRemove(t, s, mgr, `{"items":[{"hash":"`+testHash+`"},{"hash":"`+strings.ToUpper(testHash)+`"}]}`)
	if code != http.StatusOK || ans.Removed != 1 {
		t.Fatalf("duplicate: HTTP %d, %+v", code, ans)
	}
	if paths, _ := q.snapshot(); len(paths) != 1 {
		t.Errorf("duplicate: client calls = %v, want one", paths)
	}
}

// Removing downloads is staff's, like the single Delete.
func TestBulkRemoveNeedsStaff(t *testing.T) {
	s, q, _ := downloadServer(t)
	_, req := s.user(t, "req@example.com", auth.RoleRequester)
	if code, _ := bulkRemove(t, s, req, `{"items":[{"hash":"`+testHash+`"}]}`); code != http.StatusForbidden {
		t.Errorf("requester: HTTP %d, want 403", code)
	}
	if paths, _ := q.snapshot(); len(paths) != 0 {
		t.Fatalf("the client was called: %v", paths)
	}
}
