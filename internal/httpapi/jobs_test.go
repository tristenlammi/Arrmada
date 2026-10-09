package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/jobs"
)

// fakeJobs is a JobRunner that records what was submitted. With run set it runs each
// job's Fn synchronously inside Submit (so a test sees its effects straight away);
// otherwise the work is only recorded. Single-flight mirrors the real runner while a
// recorded job is "active".
type fakeJobs struct {
	mu        sync.Mutex
	run       bool
	nextID    int64
	submitted []jobs.Spec
	active    map[string]int64 // kind|target → id, for jobs not run
	results   map[int64]jobs.Job
	cancelled []int64
}

func newFakeJobs(run bool) *fakeJobs {
	return &fakeJobs{run: run, active: map[string]int64{}, results: map[int64]jobs.Job{}}
}

func (f *fakeJobs) Submit(ctx context.Context, s jobs.Spec) (int64, bool, error) {
	f.mu.Lock()
	k := s.Kind + "|" + s.Target
	if id, ok := f.active[k]; ok {
		f.mu.Unlock()
		return id, true, nil
	}
	f.nextID++
	id := f.nextID
	f.submitted = append(f.submitted, s)
	if !f.run {
		f.active[k] = id
		f.results[id] = jobs.Job{ID: id, Kind: s.Kind, Target: s.Target, Trigger: s.Trigger, Status: jobs.StatusQueued}
		f.mu.Unlock()
		return id, false, nil
	}
	f.mu.Unlock()
	p := &jobs.Progress{}
	res, err := s.Fn(ctx, p)
	j := jobs.Job{ID: id, Kind: s.Kind, Target: s.Target, Trigger: s.Trigger, Status: jobs.StatusSucceeded}
	if err != nil {
		j.Status, j.Error = jobs.StatusFailed, err.Error()
	}
	if res != nil {
		j.Result, _ = json.Marshal(res)
	}
	f.mu.Lock()
	f.results[id] = j
	f.mu.Unlock()
	return id, false, nil
}

func (f *fakeJobs) Get(_ context.Context, id int64) (jobs.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.results[id]
	if !ok {
		return jobs.Job{}, jobs.ErrNotFound
	}
	return j, nil
}

func (f *fakeJobs) List(_ context.Context, flt jobs.Filter) ([]jobs.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := []jobs.Job{}
	for id := f.nextID; id > 0; id-- {
		if j, ok := f.results[id]; ok && (flt.Kind == "" || flt.Kind == j.Kind) {
			out = append(out, j)
		}
	}
	return out, nil
}

func (f *fakeJobs) Cancel(_ context.Context, id int64) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	j, ok := f.results[id]
	if !ok {
		return jobs.ErrNotFound
	}
	if jobs.Terminal(j.Status) {
		return jobs.ErrFinished
	}
	f.cancelled = append(f.cancelled, id)
	return nil
}

func (f *fakeJobs) specs() []jobs.Spec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]jobs.Spec(nil), f.submitted...)
}

func TestJobsAPIStaffOnly(t *testing.T) {
	fj := newFakeJobs(false)
	s := newRouteServer(t, func(d *Deps) { d.Jobs = fj })
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)
	id, _, _ := fj.Submit(context.Background(), jobs.Spec{Kind: "movie.search", Target: "movie:3"})

	for _, path := range []string{"/api/v1/jobs", "/api/v1/jobs/1"} {
		if rec := s.do("GET", path, kid); rec.Code != http.StatusForbidden {
			t.Errorf("requester %s: HTTP %d, want 403", path, rec.Code)
		}
		if rec := s.do("GET", path, nil); rec.Code != http.StatusUnauthorized {
			t.Errorf("anonymous %s: HTTP %d, want 401", path, rec.Code)
		}
	}
	if rec := s.do("POST", "/api/v1/jobs/1/cancel", kid); rec.Code != http.StatusForbidden {
		t.Errorf("requester cancel: HTTP %d, want 403", rec.Code)
	}

	rec := s.do("GET", "/api/v1/jobs?kind=movie.search", mgr)
	var list struct{ Jobs []jobs.Job }
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &list) != nil || len(list.Jobs) != 1 {
		t.Fatalf("list: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.do("GET", "/api/v1/jobs?limit=x", mgr); rec.Code != http.StatusBadRequest {
		t.Errorf("bad limit: HTTP %d", rec.Code)
	}
	rec = s.do("GET", "/api/v1/jobs/1", mgr)
	var j jobs.Job
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &j) != nil || j.ID != id || j.Target != "movie:3" {
		t.Fatalf("get: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.do("GET", "/api/v1/jobs/99", mgr); rec.Code != http.StatusNotFound {
		t.Errorf("missing: HTTP %d", rec.Code)
	}
	if rec := s.do("POST", "/api/v1/jobs/1/cancel", mgr); rec.Code != http.StatusAccepted {
		t.Errorf("cancel: HTTP %d", rec.Code)
	}
	if rec := s.do("POST", "/api/v1/jobs/99/cancel", mgr); rec.Code != http.StatusNotFound {
		t.Errorf("cancel missing: HTTP %d", rec.Code)
	}
	fj.mu.Lock()
	fj.results[id] = jobs.Job{ID: id, Status: jobs.StatusSucceeded}
	fj.mu.Unlock()
	if rec := s.do("POST", "/api/v1/jobs/1/cancel", mgr); rec.Code != http.StatusConflict {
		t.Errorf("cancel finished: HTTP %d", rec.Code)
	}
}

// The real runner behind the API, end to end: a submitted job shows its final state.
func TestJobsAPIWithRealRunner(t *testing.T) {
	var r *jobs.Runner
	s := newRouteServer(t, func(d *Deps) {
		var err error
		r, err = jobs.New(context.Background(), d.Store.DB(), d.Log, nil)
		if err != nil {
			t.Fatal(err)
		}
		d.Jobs = r
	})
	t.Cleanup(func() { r.Shutdown(time.Second) })
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	id, _, err := r.Submit(context.Background(), jobs.Spec{Kind: "movie.scan", Target: "all", Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
		p.SetMessage("Added 3 movies")
		return map[string]int{"imported": 3}, nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = r.Wait(ctx, id)
	rec := s.do("GET", "/api/v1/jobs/1", mgr)
	var j jobs.Job
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &j) != nil {
		t.Fatalf("get: HTTP %d %s", rec.Code, rec.Body)
	}
	if j.Status != jobs.StatusSucceeded || j.Message != "Added 3 movies" || string(j.Result) != `{"imported":3}` || j.FinishedAt == nil {
		t.Fatalf("job = %+v", j)
	}
}
