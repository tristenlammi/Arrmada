package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/settings"
)

// Every converted POST hands its work to the job runner under the right kind, target and
// trigger, and answers 202 with the job's id. The fake runner records without running, so
// no handler here touches a real indexer, library or download client.
func TestConvertedRoutesSubmitJobs(t *testing.T) {
	fj := newFakeJobs(false)
	s := newRouteServer(t, func(d *Deps) { d.Jobs = fj })
	u, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	if err := s.deps.Settings.Set(context.Background(), settings.KeyModuleMusic, "true"); err != nil {
		t.Fatal(err)
	}
	// Block isn't here: it works out what the download is for before submitting, so it
	// needs a real coordinator — see TestBlockDownloadNamesWhatItBlocked.
	cases := []struct {
		method, path, body string
		kind, target       string
		class              string
	}{
		{"POST", "/api/v1/movies/5/search", "", "movie.search", "movie:5", jobs.ClassIndexerSearch},
		{"POST", "/api/v1/movies/5/regrab", "", "movie.regrab", "movie:5", jobs.ClassIndexerSearch},
		{"POST", "/api/v1/movies/scan", "", "movie.scan", "all", jobs.ClassLibraryScan},
		{"POST", "/api/v1/series/6/search", "", "series.search", "series:6", jobs.ClassIndexerSearch},
		{"POST", "/api/v1/series/6/autograb", `{"season":2,"episode":3}`, "series.grab-scope", "series:6:s2e3", jobs.ClassIndexerSearch},
		{"POST", "/api/v1/series/6/seasons/2/episodes/3/regrab", "", "series.regrab-episode", "series:6:s2e3", jobs.ClassIndexerSearch},
		{"POST", "/api/v1/series/scan", "", "series.scan", "all", jobs.ClassLibraryScan},
		{"POST", "/api/v1/books/7/search", "", "book.search", "book:7", jobs.ClassIndexerSearch},
		{"POST", "/api/v1/books/scan", "", "books.scan", "all", jobs.ClassLibraryScan},
		{"POST", "/api/v1/books/series-backfill", "", "books.backfill-series", "all", jobs.ClassIndexerSearch},
		{"POST", "/api/v1/music/scan", "", "music.scan", "all", "music.scan"},
	}
	for i, c := range cases {
		rec := s.doJSON(c.method, c.path, mgr, c.body)
		if rec.Code != http.StatusAccepted {
			t.Errorf("%s: HTTP %d: %s", c.path, rec.Code, rec.Body)
			continue
		}
		var body map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		specs := fj.specs()
		if len(specs) != i+1 {
			t.Fatalf("%s: %d jobs submitted in total, want %d", c.path, len(specs), i+1)
		}
		sp := specs[i]
		if sp.Kind != c.kind || sp.Target != c.target || sp.Class != c.class || sp.Trigger != fmt.Sprintf("user:%d", u.ID) || sp.Fn == nil {
			t.Errorf("%s: spec = {%s %s %s %s}, want {%s %s %s user:%d}", c.path, sp.Kind, sp.Target, sp.Class, sp.Trigger, c.kind, c.target, c.class, u.ID)
		}
		if body["job_id"] != float64(i+1) || body["existing"] != false {
			t.Errorf("%s: body = %v", c.path, body)
		}
	}
}

// Clicking Search twice produces one job: the second answer names the first job.
func TestSearchTwiceIsOneJob(t *testing.T) {
	fj := newFakeJobs(false)
	s := newRouteServer(t, func(d *Deps) { d.Jobs = fj })
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	var first, second map[string]any
	_ = json.Unmarshal(s.do("POST", "/api/v1/movies/9/search", mgr).Body.Bytes(), &first)
	rec := s.do("POST", "/api/v1/movies/9/search", mgr)
	_ = json.Unmarshal(rec.Body.Bytes(), &second)
	if rec.Code != http.StatusAccepted || second["existing"] != true || second["job_id"] != first["job_id"] {
		t.Fatalf("second click: HTTP %d %v (first %v)", rec.Code, second, first)
	}
	if n := len(fj.specs()); n != 1 {
		t.Fatalf("%d jobs submitted, want 1", n)
	}
}

// The scans that used to have their own "already running" guard keep the 409 and its
// text, now naming the running job.
func TestSingleRunScansKeepTheir409(t *testing.T) {
	fj := newFakeJobs(false)
	s := newRouteServer(t, func(d *Deps) { d.Jobs = fj })
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	if err := s.deps.Settings.Set(context.Background(), settings.KeyModuleMusic, "true"); err != nil {
		t.Fatal(err)
	}
	if rec := s.do("POST", "/api/v1/music/scan", mgr); rec.Code != http.StatusAccepted {
		t.Fatalf("first scan: HTTP %d", rec.Code)
	}
	rec := s.do("POST", "/api/v1/music/scan", mgr)
	var body map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if rec.Code != http.StatusConflict || body["message"] != "a music scan is already running" || body["job_id"] != float64(1) {
		t.Fatalf("second scan: HTTP %d %v", rec.Code, body)
	}
}

// With the real runner, two concurrent clicks while the search waits its turn (the
// indexer-search class is full) are still one job.
func TestConcurrentSearchClicksShareAJob(t *testing.T) {
	var r *jobs.Runner
	s := newRouteServer(t, func(d *Deps) {
		var err error
		r, err = jobs.New(context.Background(), d.Store.DB(), d.Log, nil)
		if err != nil {
			t.Fatal(err)
		}
		d.Jobs = r
	})
	release := make(chan struct{})
	t.Cleanup(func() { close(release); r.Shutdown(2 * time.Second) })
	// Fill the indexer-search class so the movie search stays queued.
	for i := 0; i < 2; i++ {
		_, _, _ = r.Submit(context.Background(), jobs.Spec{Kind: "hold", Target: fmt.Sprint(i), Class: jobs.ClassIndexerSearch,
			Fn: func(ctx context.Context, _ *jobs.Progress) (any, error) {
				select {
				case <-release:
				case <-ctx.Done():
				}
				return nil, nil
			}})
	}
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	type answer struct {
		code int
		body map[string]any
	}
	out := make(chan answer, 2)
	for i := 0; i < 2; i++ {
		go func() {
			rec := s.do("POST", "/api/v1/movies/4/search", mgr)
			var b map[string]any
			_ = json.Unmarshal(rec.Body.Bytes(), &b)
			out <- answer{rec.Code, b}
		}()
	}
	a1, a2 := <-out, <-out
	if a1.code != http.StatusAccepted || a2.code != http.StatusAccepted || a1.body["job_id"] != a2.body["job_id"] {
		t.Fatalf("answers = %v / %v", a1, a2)
	}
	if a1.body["existing"] == a2.body["existing"] {
		t.Fatalf("exactly one answer should be existing: %v / %v", a1.body, a2.body)
	}
	list, _ := r.List(context.Background(), jobs.Filter{Kind: "movie.search"})
	if len(list) != 1 || list[0].Status != jobs.StatusQueued {
		t.Fatalf("movie.search jobs = %+v", list)
	}
}

// The Books sweep keeps its response shape: started plus a status that already says
// running (the page starts polling on it), now with the job's id; a second click while
// it runs answers started=false and starts nothing.
func TestBookSweepKeepsItsShape(t *testing.T) {
	fj := newFakeJobs(false)
	s := newRouteServer(t, func(d *Deps) {
		d.Jobs = fj
		d.Automation = &automation.Coordinator{}
	})
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	rec := s.do("POST", "/api/v1/books/search-missing", mgr)
	var body struct {
		Started bool                       `json:"started"`
		JobID   int64                      `json:"job_id"`
		Status  automation.BookSweepStatus `json:"status"`
	}
	if rec.Code != http.StatusAccepted || json.Unmarshal(rec.Body.Bytes(), &body) != nil || !body.Started || !body.Status.Running || body.JobID != 1 {
		t.Fatalf("first: HTTP %d %s", rec.Code, rec.Body)
	}
	if sp := fj.specs(); len(sp) != 1 || sp[0].Kind != "books.search-missing" || sp[0].Target != "all" {
		t.Fatalf("specs = %+v", sp)
	}
	rec = s.do("POST", "/api/v1/books/search-missing", mgr)
	body.Started = true
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &body) != nil || body.Started || len(fj.specs()) != 1 {
		t.Fatalf("second: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.do("GET", "/api/v1/books/search-missing", mgr); rec.Code != http.StatusOK || !json.Valid(rec.Body.Bytes()) {
		t.Fatalf("status: HTTP %d", rec.Code)
	}
}

// A search job's result is the search outcome and its message the sentence the button
// shows; a title already being searched ends succeeded with that reason.
func TestSearchJobResultAndMessage(t *testing.T) {
	st := newRouteServer(t, nil)
	r, err := jobs.New(context.Background(), st.deps.Store.DB(), st.deps.Log, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { r.Shutdown(time.Second) })
	run := func(fn func(context.Context) (automation.SearchOutcome, error)) jobs.Job {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		j, err := r.Run(ctx, jobs.Spec{Kind: "movie.search", Target: "movie:1", Fn: outcomeFn("movie", fn)})
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	j := run(func(context.Context) (automation.SearchOutcome, error) {
		return automation.SearchOutcome{Searched: true, Returned: 12, Reason: automation.ReasonNoneForTitle}, nil
	})
	var out automation.SearchOutcome
	if j.Status != jobs.StatusSucceeded || j.Message != "12 releases found, none for this movie" || json.Unmarshal(j.Result, &out) != nil || out.Returned != 12 {
		t.Fatalf("job = %+v", j)
	}
	j = run(func(context.Context) (automation.SearchOutcome, error) {
		return automation.SearchOutcome{}, automation.ErrAlreadySearching
	})
	if j.Status != jobs.StatusSucceeded || !strings.HasPrefix(j.Message, "Already being searched") {
		t.Fatalf("already-searching job = %+v", j)
	}
	j = run(func(context.Context) (automation.SearchOutcome, error) {
		return automation.SearchOutcome{Searched: true}, errors.New("every indexer failed")
	})
	if j.Status != jobs.StatusFailed || j.Error != "every indexer failed" {
		t.Fatalf("failed job = %+v", j)
	}
}
