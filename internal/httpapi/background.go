package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/safego"
)

// submit runs work a request kicked off (a search, a scan, an import) as a job, after the
// response has gone, so the browser isn't held open for minutes.
//
// The job runner records it (GET /api/v1/jobs/{id} shows how it ended), keeps it to one
// copy per (kind, target) — a second click on Search gets the first click's job back,
// existing=true — limits how many searches hit the indexers at once, and cancels it at
// shutdown. A panic in it is contained. Trigger defaults to who asked ("user:<id>").
//
// Without a runner (tests, tools) the work still runs on the run group, panic-safe and
// cancelled at shutdown, just unrecorded; id is then 0.
func (a *api) submit(r *http.Request, spec jobs.Spec) (id int64, existing bool, err error) {
	if spec.Trigger == "" && r != nil {
		spec.Trigger = triggerFor(r)
	}
	if a.deps.Jobs != nil {
		id, existing, err = a.deps.Jobs.Submit(a.runCtx(), spec)
		if err != nil {
			a.deps.Log.Warn("couldn't start background work", "kind", spec.Kind, "target", spec.Target, "err", err)
		}
		return id, existing, err
	}
	name := spec.Kind
	if spec.Target != "" {
		name += " (" + spec.Target + ")"
	}
	run := func(parent context.Context) {
		ctx, cancel := parent, context.CancelFunc(func() {})
		if spec.Timeout > 0 {
			ctx, cancel = context.WithTimeout(parent, spec.Timeout)
		}
		defer cancel()
		if _, err := spec.Fn(ctx, nil); err != nil {
			a.deps.Log.Warn(spec.Kind+" failed", "target", spec.Target, "err", err)
		}
	}
	if g := a.deps.RunGroup; g != nil {
		g.Go(name, run)
		return 0, false, nil
	}
	safego.Go(a.deps.Log, name, func() { run(a.runCtx()) })
	return 0, false, nil
}

// jobSubmitter is the runner for modules that start jobs themselves (the books re-match
// and sweep); nil without one, which those modules handle by running the work untracked.
func (a *api) jobSubmitter() jobs.Submitter {
	if a.deps.Jobs == nil {
		return nil
	}
	return a.deps.Jobs
}

// submitOr503 is submit for a handler whose whole answer is "started": on failure it
// writes 503 and returns ok=false.
func (a *api) submitOr503(w http.ResponseWriter, r *http.Request, spec jobs.Spec) (id int64, existing, ok bool) {
	id, existing, err := a.submit(r, spec)
	if err != nil {
		a.writeError(w, http.StatusServiceUnavailable, "couldn't start that just now — try again in a moment")
		return 0, false, false
	}
	return id, existing, true
}

// accepted answers 202 for work that was started (or was already running): the given
// fields plus job_id and existing, so the page can follow the job.
func (a *api) accepted(w http.ResponseWriter, id int64, existing bool, fields map[string]any) {
	body := map[string]any{}
	for k, v := range fields {
		body[k] = v
	}
	if id > 0 {
		body["job_id"] = id
	}
	body["existing"] = existing
	a.writeJSON(w, http.StatusAccepted, body)
}

// alreadyRunning is the 409 for work that may only run once at a time (refresh all, a
// library scan, an import), naming the job that is running so the page can follow it.
func (a *api) alreadyRunning(w http.ResponseWriter, id int64, message string) {
	body := map[string]any{"status": "error", "message": message, "existing": true}
	if id > 0 {
		body["job_id"] = id
	}
	a.writeJSON(w, http.StatusConflict, body)
}

// errFn adapts work that only returns an error to a job function.
func errFn(fn func(ctx context.Context) error) func(context.Context, *jobs.Progress) (any, error) {
	return func(ctx context.Context, _ *jobs.Progress) (any, error) { return nil, fn(ctx) }
}

// interactive marks a job's searches as a person's own (a Search, Block or Regrab
// button): they still ask an indexer that background sweeps are leaving alone after
// repeated failures, so a fixed indexer is noticed and one success clears its pause. Only
// for work about one title — a job that searches a whole author or library is a sweep.
func interactive(fn func(context.Context, *jobs.Progress) (any, error)) func(context.Context, *jobs.Progress) (any, error) {
	return func(ctx context.Context, p *jobs.Progress) (any, error) {
		return fn(automation.WithDefaultSearchTrigger(indexer.WithInteractive(ctx), automation.TriggerManual), p)
	}
}

// triggered says what started a search job, for its recorded attempt ("add" when a title
// was just added). Without it a person's search records "manual".
func triggered(trigger string, spec jobs.Spec) jobs.Spec {
	fn := spec.Fn
	spec.Fn = func(ctx context.Context, p *jobs.Progress) (any, error) {
		return fn(automation.WithSearchTrigger(ctx, trigger), p)
	}
	return spec
}

// searchFn adapts a title search: finding the title already being searched (by the sweep
// or another click) is not a failure — the search in flight covers it.
func searchFn(fn func(ctx context.Context) error) func(context.Context, *jobs.Progress) (any, error) {
	return func(ctx context.Context, p *jobs.Progress) (any, error) {
		err := fn(ctx)
		if errors.Is(err, automation.ErrAlreadySearching) {
			p.SetMessage("Already being searched")
			return nil, nil
		}
		return nil, err
	}
}

// outcomeFn adapts a title search that reports what it found: the outcome is the job's
// result and its plain sentence the job's message ("Grabbed …", "No releases found"),
// which the Search button shows when the job ends. Finding the title already being
// searched is a success with that reason. Every such search is a person's (see
// interactive).
func outcomeFn(noun string, fn func(ctx context.Context) (automation.SearchOutcome, error)) func(context.Context, *jobs.Progress) (any, error) {
	return outcomeJobFn(noun, true, fn)
}

// bulkOutcomeFn is outcomeFn for one title of a bulk search (Wanted's Search all): still
// a person's search, but not an interactive one — like a sweep it leaves alone the
// indexers that are backing off after repeated failures.
func bulkOutcomeFn(noun string, fn func(ctx context.Context) (automation.SearchOutcome, error)) func(context.Context, *jobs.Progress) (any, error) {
	return outcomeJobFn(noun, false, fn)
}

func outcomeJobFn(noun string, interactiveSearch bool, fn func(ctx context.Context) (automation.SearchOutcome, error)) func(context.Context, *jobs.Progress) (any, error) {
	return func(ctx context.Context, p *jobs.Progress) (any, error) {
		if interactiveSearch {
			ctx = indexer.WithInteractive(ctx)
		}
		out, err := fn(automation.WithDefaultSearchTrigger(ctx, automation.TriggerManual))
		if errors.Is(err, automation.ErrAlreadySearching) {
			out.Reason, err = automation.ReasonAlreadySearching, nil
		}
		if err != nil {
			return out, err
		}
		p.SetMessage(out.Message(noun))
		return out, nil
	}
}

// The job specs for the searches several handlers start.

func (a *api) movieSearchJob(id int64) jobs.Spec {
	return jobs.Spec{Kind: "movie.search", Target: jobTarget("movie", id), Class: jobs.ClassIndexerSearch, Timeout: 3 * time.Minute,
		Fn: outcomeFn("movie", func(ctx context.Context) (automation.SearchOutcome, error) {
			return a.deps.Automation.SearchMovie(ctx, id)
		})}
}

func (a *api) movieUpgradeJob(id int64) jobs.Spec {
	return jobs.Spec{Kind: "movie.upgrade", Target: jobTarget("movie", id), Class: jobs.ClassIndexerSearch, Timeout: 3 * time.Minute,
		Fn: interactive(searchFn(func(ctx context.Context) error { return a.deps.Automation.UpgradeMovie(ctx, id) }))}
}

// movieBulkSearchJob is one title of Wanted → Missing's Search all (movieSearchJob, not
// interactive). Its single-flight key is the plain search's, so a title already queued
// by a click isn't queued twice.
func (a *api) movieBulkSearchJob(id int64) jobs.Spec {
	spec := a.movieSearchJob(id)
	spec.Fn = bulkOutcomeFn("movie", func(ctx context.Context) (automation.SearchOutcome, error) {
		return a.deps.Automation.SearchMovie(ctx, id)
	})
	return spec
}

// movieBulkUpgradeJob is one title of Wanted → Cutoff unmet's Search all: an upgrade
// search under the batch's shared upgrade budget, not interactive. Recorded as an upgrade
// search, like the sweep's, so one that finds nothing better adds no History line.
func (a *api) movieBulkUpgradeJob(id int64, batch *automation.UpgradeBatch) jobs.Spec {
	spec := a.movieUpgradeJob(id)
	spec.Fn = searchFn(func(ctx context.Context) error {
		return a.deps.Automation.UpgradeMovieIn(ctx, id, batch)
	})
	return spec
}

func (a *api) movieRegrabJob(id int64) jobs.Spec {
	return jobs.Spec{Kind: "movie.regrab", Target: jobTarget("movie", id), Class: jobs.ClassIndexerSearch, Timeout: 3 * time.Minute,
		Fn: interactive(errFn(func(ctx context.Context) error { return a.deps.Automation.RegrabMovie(ctx, id) }))}
}

// enqueueMovie puts a movie search job on the movie search queue (automation's
// EnqueueMovieSearch): the job runner's indexer-search class, two at a time, with the
// sweeps leaving the movie alone until it has run. Every movie search a request starts
// goes through here rather than a.submit. Trigger defaults to who asked.
func (a *api) enqueueMovie(r *http.Request, id int64, spec jobs.Spec) (automation.MovieQueued, error) {
	if spec.Trigger == "" && r != nil {
		spec.Trigger = triggerFor(r)
	}
	q, err := a.deps.Automation.EnqueueMovieSearch(a.runCtx(), a.jobSubmitter(), id, spec)
	if err != nil {
		a.deps.Log.Warn("couldn't queue a movie search", "kind", spec.Kind, "movie", id, "err", err)
	}
	return q, err
}

// acceptedQueued answers 202 for a queued movie search: fields plus job_id, existing,
// queued and the search's place in the queue (0 = running now).
func (a *api) acceptedQueued(w http.ResponseWriter, q automation.MovieQueued, fields map[string]any) {
	body := map[string]any{"queued": true, "position": q.Position}
	for k, v := range fields {
		body[k] = v
	}
	a.accepted(w, q.JobID, q.Existing, body)
}

func (a *api) seriesSearchJob(id int64) jobs.Spec {
	return jobs.Spec{Kind: "series.search", Target: jobTarget("series", id), Class: jobs.ClassIndexerSearch, Timeout: 5 * time.Minute,
		Fn: outcomeFn("show", func(ctx context.Context) (automation.SearchOutcome, error) {
			return a.deps.Automation.SearchSeriesNow(ctx, id)
		})}
}

func (a *api) bookSearchJob(id int64) jobs.Spec {
	return jobs.Spec{Kind: "book.search", Target: jobTarget("book", id), Class: jobs.ClassIndexerSearch, Timeout: 5 * time.Minute,
		Fn: outcomeFn("book", func(ctx context.Context) (automation.SearchOutcome, error) {
			return a.deps.Automation.SearchBookNow(ctx, id)
		})}
}

func (a *api) albumSearchJob(id int64) jobs.Spec {
	return jobs.Spec{Kind: "album.search", Target: jobTarget("album", id), Class: jobs.ClassIndexerSearch, Timeout: 5 * time.Minute,
		Fn: outcomeFn("album", func(ctx context.Context) (automation.SearchOutcome, error) {
			return a.deps.Automation.SearchAlbumNow(ctx, id)
		})}
}

// movieManualSearchJob is a person's Search of one movie (the movie page, the Wanted
// view): the movie search job, but a movie already downloading is left alone
// (SearchMovieManual) rather than grabbed twice. Same kind and target as movieSearchJob,
// so a click on either page while the other's search runs gets that job back.
func (a *api) movieManualSearchJob(id int64) jobs.Spec {
	spec := a.movieSearchJob(id)
	spec.Fn = outcomeFn("movie", func(ctx context.Context) (automation.SearchOutcome, error) {
		return a.deps.Automation.SearchMovieManual(ctx, id)
	})
	return spec
}

// seriesManualSearchJob is a person's Search of one show: it holds back the seasons the
// client is still downloading (SearchSeriesManual), as the sweep does.
func (a *api) seriesManualSearchJob(id int64) jobs.Spec {
	spec := a.seriesSearchJob(id)
	spec.Fn = outcomeFn("show", func(ctx context.Context) (automation.SearchOutcome, error) {
		return a.deps.Automation.SearchSeriesManual(ctx, id)
	})
	return spec
}

// scanSummary is a library scan's job result: the counts. The folders it couldn't match
// (with their candidates) stay on each library's Unmatched list, not in the jobs table.
type scanSummary struct {
	Imported  int `json:"imported"`
	Skipped   int `json:"skipped"`
	Unmatched int `json:"unmatched"`
}

// scanMessage words a finished scan for the page's toast: "Added 3 movies; 1 folder
// wasn't recognised".
func scanMessage(noun string, added, unmatched int) string {
	msg := "Nothing new found"
	if added > 0 {
		msg = "Added " + countOf(added, noun)
	}
	if unmatched > 0 {
		msg += "; " + countOf(unmatched, "folder") + " not recognised"
	}
	return msg
}

func countOf(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	if strings.HasSuffix(noun, "s") {
		return strconv.Itoa(n) + " " + noun
	}
	return strconv.Itoa(n) + " " + noun + "s"
}

// runCtx is the context for work that must outlive the request that started it: the
// run group's (cancelled at shutdown) when there is one.
func (a *api) runCtx() context.Context {
	if g := a.deps.RunGroup; g != nil {
		return g.Context()
	}
	return context.Background()
}

// triggerFor says who started request-triggered work, for the job record and the task
// history: "user:<id>" for a signed-in person, "api" otherwise.
func triggerFor(r *http.Request) string {
	if u, ok := userFrom(r); ok && u != nil {
		return "user:" + strconv.FormatInt(u.ID, 10)
	}
	return "api"
}

// jobTarget formats a job target, e.g. jobTarget("movie", 12) = "movie:12".
func jobTarget(kind string, id int64) string { return kind + ":" + strconv.FormatInt(id, 10) }
