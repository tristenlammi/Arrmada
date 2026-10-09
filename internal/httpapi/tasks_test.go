package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/scheduler"
)

func newTaskServer(t *testing.T) (*routeServer, chan struct{}) {
	t.Helper()
	release := make(chan struct{})
	var sched *scheduler.Scheduler
	s := newRouteServer(t, func(d *Deps) {
		sched = scheduler.New(d.Log)
		sched.Register("rss-sync", 15*time.Minute, false, func(ctx context.Context) error {
			select {
			case <-release:
			case <-ctx.Done():
			}
			return errors.New("indexer down")
		}, scheduler.Label("Check indexer feeds for new movies"), scheduler.Description("Polls each indexer's recent uploads."))
		sched.Register("heartbeat", time.Hour, false, func(context.Context) error { return nil }, scheduler.Hidden())
		d.Scheduler = sched
	})
	ctx, cancel := context.WithCancel(context.Background())
	sched.Start(ctx)
	t.Cleanup(func() {
		select {
		case <-release:
		default:
			close(release)
		}
		cancel()
		sched.Wait()
	})
	return s, release
}

func TestTasksListIsStaffOnlyAndHidesPlumbing(t *testing.T) {
	s, _ := newTaskServer(t)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)

	if rec := s.do("GET", "/api/v1/system/tasks", kid); rec.Code != http.StatusForbidden {
		t.Fatalf("requester: HTTP %d, want 403", rec.Code)
	}
	if rec := s.do("GET", "/api/v1/system/tasks", nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous: HTTP %d, want 401", rec.Code)
	}
	rec := s.do("GET", "/api/v1/system/tasks", mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("manager: HTTP %d: %s", rec.Code, rec.Body)
	}
	var tasks []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &tasks); err != nil {
		t.Fatalf("body isn't a bare array: %v: %s", err, rec.Body)
	}
	if len(tasks) != 1 || tasks[0]["name"] != "rss-sync" || tasks[0]["label"] != "Check indexer feeds for new movies" {
		t.Fatalf("tasks = %+v", tasks)
	}
	// The contract the Status page reads.
	for _, k := range []string{"name", "interval_seconds", "last_start", "last_duration_ms", "last_error", "runs", "failures", "running", "next_run"} {
		if _, ok := tasks[0][k]; !ok {
			t.Errorf("task is missing %q: %+v", k, tasks[0])
		}
	}
	if tasks[0]["interval_seconds"] != float64(900) {
		t.Errorf("interval_seconds = %v", tasks[0]["interval_seconds"])
	}
}

func TestRunTaskAdminOnlyBusyAndUnknown(t *testing.T) {
	s, release := newTaskServer(t)
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)

	for name, c := range map[string]*http.Cookie{"manager": mgr, "requester": kid} {
		if rec := s.do("POST", "/api/v1/system/tasks/rss-sync/run", c); rec.Code != http.StatusForbidden {
			t.Errorf("%s: HTTP %d, want 403", name, rec.Code)
		}
	}
	if rec := s.do("POST", "/api/v1/system/tasks/nope/run", admin); rec.Code != http.StatusNotFound {
		t.Errorf("unknown: HTTP %d, want 404", rec.Code)
	}
	if rec := s.do("POST", "/api/v1/system/tasks/heartbeat/run", admin); rec.Code != http.StatusNotFound {
		t.Errorf("hidden: HTTP %d, want 404", rec.Code)
	}
	if rec := s.do("POST", "/api/v1/system/tasks/rss-sync/run", admin); rec.Code != http.StatusAccepted {
		t.Fatalf("run: HTTP %d: %s", rec.Code, rec.Body)
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		if st, _ := s.deps.Scheduler.Status("rss-sync"); st.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the task never started")
		}
		time.Sleep(time.Millisecond)
	}
	if rec := s.do("POST", "/api/v1/system/tasks/rss-sync/run", admin); rec.Code != http.StatusConflict {
		t.Fatalf("second run while busy: HTTP %d, want 409", rec.Code)
	}
	close(release)
	deadline = time.Now().Add(2 * time.Second)
	for {
		st, _ := s.deps.Scheduler.Status("rss-sync")
		if !st.Running && st.Runs == 1 {
			if st.LastError != "indexer down" || st.ConsecutiveFailures != 1 {
				t.Fatalf("status = %+v", st)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the run never finished")
		}
		time.Sleep(time.Millisecond)
	}
}
