package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/jobs"
)

// insightsImportServer is the router with a real Insights service and job runner, and a
// recorded database copy.
func insightsImportServer(t *testing.T, snap *snapshotRecorder) (*routeServer, *jobs.Runner) {
	t.Helper()
	var r *jobs.Runner
	s := newRouteServer(t, func(d *Deps) {
		var err error
		if r, err = jobs.New(context.Background(), d.Store.DB(), d.Log, nil); err != nil {
			t.Fatal(err)
		}
		d.Jobs = r
		d.Insights = insights.NewService(d.Store.DB(), d.Settings, nil, nil, d.Log)
		if snap != nil {
			d.Snapshot = snap.fn
		}
	})
	t.Cleanup(func() { r.Shutdown(2 * time.Second) })
	return s, r
}

// seedDoubleCount stores one live play and the imported copy of it, plus an imported play
// nothing recorded live.
func seedDoubleCount(t *testing.T, s *routeServer) {
	t.Helper()
	if _, err := s.st.DB().Exec(`INSERT INTO stream_sessions (session_key,user_id,rating_key,title,started_at,stopped_at)
		VALUES ('42','7','100','Dune',1003,4010), ('','7','100','Dune',1000,4005), ('','7','100','Dune',90000,93000)`); err != nil {
		t.Fatal(err)
	}
}

func (s *routeServer) sessionCount(t *testing.T) int {
	t.Helper()
	var n int
	if err := s.st.DB().QueryRow(`SELECT COUNT(*) FROM stream_sessions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func waitJob(t *testing.T, r *jobs.Runner, rec *http.Response, body []byte) jobs.Job {
	t.Helper()
	var ans struct {
		JobID int64 `json:"job_id"`
	}
	if err := json.Unmarshal(body, &ans); err != nil || ans.JobID == 0 {
		t.Fatalf("HTTP %d %s: no job id", rec.StatusCode, body)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := r.Wait(ctx, ans.JobID); err != nil {
		t.Fatal(err)
	}
	j, err := r.Get(ctx, ans.JobID)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

func TestRemoveImportOverlapsBacksUpFirst(t *testing.T) {
	snap := &snapshotRecorder{}
	s, r := insightsImportServer(t, snap)
	seedDoubleCount(t, s)
	before := 0
	snap.check = func() bool { before = s.sessionCount(t); return true }
	_, admin := s.user(t, "owner@example.com", auth.RoleAdmin)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)

	if rec := s.do("GET", "/api/v1/insights/import/overlaps", mgr); rec.Code != http.StatusForbidden {
		t.Errorf("manager count: HTTP %d, want 403 (admin only)", rec.Code)
	}
	rec := s.do("GET", "/api/v1/insights/import/overlaps", admin)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"count":1`) || !strings.Contains(rec.Body.String(), `"first_live_at":1003`) {
		t.Fatalf("count: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.doJSON("POST", "/api/v1/insights/import/overlaps/remove", mgr, `{"expected":1}`); rec.Code != http.StatusForbidden {
		t.Errorf("manager remove: HTTP %d, want 403", rec.Code)
	}
	if rec := s.doJSON("POST", "/api/v1/insights/import/overlaps/remove", admin, `{}`); rec.Code != http.StatusBadRequest {
		t.Errorf("no expected count: HTTP %d, want 400", rec.Code)
	}

	rec = s.doJSON("POST", "/api/v1/insights/import/overlaps/remove", admin, `{"expected":1}`)
	if j := waitJob(t, r, rec.Result(), rec.Body.Bytes()); j.Status != jobs.StatusSucceeded {
		t.Fatalf("job %s: %s", j.Status, j.Error)
	}
	if len(snap.calls) != 1 || snap.calls[0] != "pre-insights-repair" || before != 3 {
		t.Fatalf("snapshots %v taken with %d rows present; want one pre-insights-repair before anything was deleted", snap.calls, before)
	}
	if n := s.sessionCount(t); n != 2 {
		t.Errorf("%d rows left, want 2 (the live play and the import nobody else saw)", n)
	}
}

// fakeTautulli serves n history rows (user 7, one play every 10 000 s) in Tautulli's
// get_history shape, honouring start/length, and records each request's query.
func fakeTautulli(t *testing.T, n int, key string) (*httptest.Server, func() []string) {
	t.Helper()
	var mu sync.Mutex
	var queries []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		mu.Lock()
		queries = append(queries, r.URL.RawQuery)
		mu.Unlock()
		if q.Get("apikey") != key {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		start, _ := strconv.Atoi(q.Get("start"))
		length, _ := strconv.Atoi(q.Get("length"))
		rows := []map[string]any{}
		for i := start; i < n && i < start+length; i++ {
			began := 1_000_000 + i*10_000
			rows = append(rows, map[string]any{"user_id": 7, "user": "amy", "title": fmt.Sprintf("Film %d", i),
				"media_type": "movie", "rating_key": strconv.Itoa(i), "started": began, "stopped": began + 3000, "duration": 2900})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"response": map[string]any{"result": "success",
			"data": map[string]any{"recordsFiltered": n, "data": rows}}})
	}))
	t.Cleanup(srv.Close)
	return srv, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), queries...)
	}
}

func (s *routeServer) importRuns(t *testing.T, c *http.Cookie) []insights.ImportRun {
	t.Helper()
	rec := s.do("GET", "/api/v1/insights/import/runs", c)
	var body struct {
		Runs []insights.ImportRun `json:"runs"`
	}
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil {
		t.Fatalf("runs: HTTP %d %s", rec.Code, rec.Body)
	}
	return body.Runs
}

// A 1,200-play history over three pages: progress reaches the total, the counts add up, a
// Retry adds nothing, and undo removes exactly that run's plays.
func TestImportRunProgressRetryAndUndo(t *testing.T) {
	const key = "s3cr3t-tautulli-key"
	srv, queries := fakeTautulli(t, 1200, key)
	snap := &snapshotRecorder{}
	s, r := insightsImportServer(t, snap)
	// Arrmada recorded play #3 live.
	if _, err := s.st.DB().Exec(`INSERT INTO stream_sessions (session_key,user_id,rating_key,title,started_at,stopped_at)
		VALUES ('9','7','3','Film 3',1030005,1032990)`); err != nil {
		t.Fatal(err)
	}
	_, admin := s.user(t, "owner@example.com", auth.RoleAdmin)

	rec := s.doJSON("POST", "/api/v1/insights/import/tautulli", admin, `{"url":"`+srv.URL+`","api_key":"`+key+`"}`)
	if j := waitJob(t, r, rec.Result(), rec.Body.Bytes()); j.Status != jobs.StatusSucceeded || strings.Contains(string(j.Result), key) {
		t.Fatalf("import job %s %q result %s", j.Status, j.Error, j.Result)
	}
	runs := s.importRuns(t, admin)
	if len(runs) != 1 {
		t.Fatalf("runs = %+v", runs)
	}
	got := runs[0]
	if got.Status != insights.RunDone || got.Total != 1200 || got.Processed != 1200 || got.Imported != 1199 || got.Overlaps != 1 || got.Rows != 1199 {
		t.Fatalf("run = %+v; want done, 1200/1200, 1199 imported, 1 recorded live", got)
	}
	for _, q := range queries() {
		if strings.Contains(q, "cmd=get_history") && !strings.Contains(q, "grouping=0") {
			t.Errorf("history asked for grouped rows: %s", q)
		}
	}

	// Retry from the saved connection (no key in the request): nothing new comes in.
	rec = s.do("POST", fmt.Sprintf("/api/v1/insights/import/runs/%d/retry", got.ID), admin)
	if j := waitJob(t, r, rec.Result(), rec.Body.Bytes()); j.Status != jobs.StatusSucceeded {
		t.Fatalf("retry job %s %q", j.Status, j.Error)
	}
	runs = s.importRuns(t, admin)
	if len(runs) != 2 || runs[0].Imported != 0 || runs[0].Duplicates != 1199 || runs[0].Overlaps != 1 {
		t.Fatalf("retry run = %+v; want nothing imported", runs[0])
	}
	if n := s.sessionCount(t); n != 1200 {
		t.Fatalf("%d plays after the retry, want 1200", n)
	}

	// Undo the first run: a backup first, then exactly its 1,199 plays go.
	if rec := s.do("DELETE", fmt.Sprintf("/api/v1/insights/import/runs/%d/rows", got.ID), admin); rec.Code != http.StatusBadRequest {
		t.Errorf("undo without a confirmed count: HTTP %d, want 400", rec.Code)
	}
	rec = s.do("DELETE", fmt.Sprintf("/api/v1/insights/import/runs/%d/rows?expected=1199", got.ID), admin)
	if j := waitJob(t, r, rec.Result(), rec.Body.Bytes()); j.Status != jobs.StatusSucceeded {
		t.Fatalf("undo job %s %q", j.Status, j.Error)
	}
	if len(snap.calls) != 1 || snap.calls[0] != "pre-insights-repair" {
		t.Errorf("snapshots = %v, want one before the undo", snap.calls)
	}
	if n := s.sessionCount(t); n != 1 {
		t.Errorf("%d plays after undo, want only the live one", n)
	}

	// The key never comes back from any endpoint.
	for _, path := range []string{"/api/v1/insights/import/tautulli", "/api/v1/insights/import/runs"} {
		rec := s.do("GET", path, admin)
		if rec.Code != http.StatusOK || strings.Contains(rec.Body.String(), key) {
			t.Errorf("%s: HTTP %d, leaks the key: %s", path, rec.Code, rec.Body)
		}
	}
	if !strings.Contains(s.do("GET", "/api/v1/insights/import/tautulli", admin).Body.String(), `"api_key_set":true`) {
		t.Error("config doesn't say a key is saved")
	}
}

// The saved key is only sent to the address it was saved for.
func TestImportSavedKeyStaysWithItsURL(t *testing.T) {
	const key = "s3cr3t-tautulli-key"
	srv, _ := fakeTautulli(t, 0, key)
	other, otherQueries := fakeTautulli(t, 0, "whatever")
	s, r := insightsImportServer(t, nil)
	_, admin := s.user(t, "owner@example.com", auth.RoleAdmin)
	rec := s.doJSON("POST", "/api/v1/insights/import/tautulli", admin, `{"url":"`+srv.URL+`","api_key":"`+key+`"}`)
	waitJob(t, r, rec.Result(), rec.Body.Bytes())

	if rec := s.doJSON("POST", "/api/v1/insights/import/tautulli", admin, `{"url":"`+other.URL+`"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("new URL without a key: HTTP %d, want 400", rec.Code)
	}
	if q := otherQueries(); len(q) != 0 {
		t.Errorf("the saved key was sent to another address: %v", q)
	}
}

// No backup, no delete; and a count that changed since the owner confirmed deletes nothing.
func TestRemoveImportOverlapsRefusesUnsafely(t *testing.T) {
	snap := &snapshotRecorder{fail: errors.New("disk full")}
	s, r := insightsImportServer(t, snap)
	seedDoubleCount(t, s)
	_, admin := s.user(t, "owner@example.com", auth.RoleAdmin)

	rec := s.doJSON("POST", "/api/v1/insights/import/overlaps/remove", admin, `{"expected":1}`)
	if j := waitJob(t, r, rec.Result(), rec.Body.Bytes()); j.Status != jobs.StatusFailed || !strings.Contains(j.Error, "nothing was deleted") {
		t.Fatalf("job %s %q, want failed saying nothing was deleted", j.Status, j.Error)
	}
	if n := s.sessionCount(t); n != 3 {
		t.Fatalf("%d rows left after a failed backup, want all 3", n)
	}

	snap.fail = nil
	rec = s.doJSON("POST", "/api/v1/insights/import/overlaps/remove", admin, `{"expected":4}`)
	if j := waitJob(t, r, rec.Result(), rec.Body.Bytes()); j.Status != jobs.StatusFailed {
		t.Fatalf("stale count: job %s, want failed", j.Status)
	}
	if n := s.sessionCount(t); n != 3 {
		t.Fatalf("%d rows left after a stale confirmation, want all 3", n)
	}

	// Without any way to take a copy, the request is refused outright.
	s2, _ := insightsImportServer(t, nil)
	_, admin2 := s2.user(t, "owner@example.com", auth.RoleAdmin)
	if rec := s2.doJSON("POST", "/api/v1/insights/import/overlaps/remove", admin2, `{"expected":0}`); rec.Code != http.StatusInternalServerError {
		t.Errorf("no snapshot func: HTTP %d, want 500", rec.Code)
	}
}
