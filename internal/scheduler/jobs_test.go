package scheduler

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/store"
)

func runnerFor(t *testing.T) *jobs.Runner {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	r, err := jobs.New(context.Background(), st.DB(), quietLogger(), nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Shutdown(2 * time.Second) })
	return r
}

func waitJobDone(t *testing.T, r *jobs.Runner, id int64) jobs.Job {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := r.Wait(ctx, id); err != nil {
		t.Fatal(err)
	}
	j, err := r.Get(context.Background(), id)
	if err != nil {
		t.Fatal(err)
	}
	return j
}

// Run now on an idle task makes a jobs row that ends with the task's outcome.
func TestRunNowRecordsAJob(t *testing.T) {
	r := runnerFor(t)
	s := New(quietLogger())
	s.SetJobs(ViaJobs(context.Background(), r))
	s.Register("rss-sync", time.Hour, false, func(context.Context) error { return errors.New("no indexers answered") })
	s.Register("crashy", time.Hour, false, func(context.Context) error { panic("nil map") })
	s.Register("fine", time.Hour, false, func(context.Context) error { return nil })
	started(t, s)

	id, existing, err := s.RunNow("rss-sync", "user:1")
	if err != nil || existing || id == 0 {
		t.Fatalf("RunNow = %d %v %v", id, existing, err)
	}
	j := waitJobDone(t, r, id)
	if j.Status != jobs.StatusFailed || j.Error != "no indexers answered" || j.Kind != JobKind || j.Target != "rss-sync" || j.Trigger != "user:1" {
		t.Fatalf("job = %+v", j)
	}
	if st := statusOf(t, s, "rss-sync"); st.LastError != "no indexers answered" || st.Runs != 1 || st.Running || st.JobID != 0 {
		t.Fatalf("task = %+v", st)
	}

	id, _, _ = s.RunNow("crashy", "")
	if j := waitJobDone(t, r, id); j.Status != jobs.StatusPanicked || !strings.Contains(j.Error, "nil map") {
		t.Fatalf("panicking job = %+v", j)
	}
	if st := statusOf(t, s, "crashy"); st.LastStatus != StatusPanicked {
		t.Fatalf("panicking task = %+v", st)
	}

	id, _, _ = s.RunNow("fine", "")
	if j := waitJobDone(t, r, id); j.Status != jobs.StatusSucceeded {
		t.Fatalf("ok job = %+v", j)
	}
}

// Run now while the ticker's own run is in flight doesn't run the task a second time,
// and a tick during a Run now is skipped.
func TestRunNowAndTickNeverOverlap(t *testing.T) {
	r := runnerFor(t)
	s := New(quietLogger())
	s.SetJobs(ViaJobs(context.Background(), r))
	release := make(chan struct{})
	var runs int32
	s.Register("search-missing-movies", time.Hour, false, func(ctx context.Context) error {
		atomic.AddInt32(&runs, 1)
		<-release
		return nil
	})
	started(t, s)
	tk := s.find("search-missing-movies")

	tickDone := make(chan struct{})
	go func() { s.exec(context.Background(), tk, TriggerSchedule); close(tickDone) }()
	waitFor(t, "the tick to start", func() bool { return atomic.LoadInt32(&runs) == 1 })
	if _, _, err := s.RunNow("search-missing-movies", ""); !errors.Is(err, ErrBusy) {
		t.Fatalf("RunNow during a tick = %v, want ErrBusy", err)
	}
	release <- struct{}{}
	<-tickDone
	if got := atomic.LoadInt32(&runs); got != 1 {
		t.Fatalf("runs = %d, want 1", got)
	}

	// Now the other way round.
	id, _, err := s.RunNow("search-missing-movies", "")
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, "the Run now to start", func() bool { return atomic.LoadInt32(&runs) == 2 })
	s.exec(context.Background(), tk, TriggerSchedule) // a tick lands mid-run
	close(release)
	waitJobDone(t, r, id)
	if got := atomic.LoadInt32(&runs); got != 2 {
		t.Fatalf("runs = %d, want 2", got)
	}
	if st := statusOf(t, s, "search-missing-movies"); st.Skipped != 1 || st.Runs != 2 {
		t.Fatalf("status = %+v", st)
	}
}
