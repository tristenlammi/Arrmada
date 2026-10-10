package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
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
