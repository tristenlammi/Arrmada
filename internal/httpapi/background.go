package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/tristenlammi/arrmada/internal/safego"
)

// bg runs work a request kicked off (a search, a scan, an import) after the response
// has gone, so the browser isn't held open for minutes.
//
// It never uses context.Background(): the work gets the app's run context, bounded by
// timeout, so shutdown cancels it instead of Docker killing it halfway, and it is tracked
// by the run group, which names anything still going when the process exits. A panic in
// it is logged with its stack and stays inside it — the HTTP middleware's recover can't
// reach a goroutine the handler started. kind says what the work is; target which item
// (e.g. "movie 12"), and both go on the failure line. The signature is deliberately what
// a job runner needs, so one can be swapped in here later without touching handlers.
func (a *api) bg(kind, target string, timeout time.Duration, fn func(ctx context.Context) error) {
	name := kind
	if target != "" {
		name = kind + " (" + target + ")"
	}
	run := func(parent context.Context) {
		ctx, cancel := context.WithTimeout(parent, timeout)
		defer cancel()
		if err := fn(ctx); err != nil {
			a.deps.Log.Warn(kind+" failed", "target", target, "err", err)
		}
	}
	if g := a.deps.RunGroup; g != nil {
		g.Go(name, run)
		return
	}
	// No run group (tests, tools): still panic-safe, just not tied to shutdown.
	safego.Go(a.deps.Log, name, func() { run(context.Background()) })
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

// idTarget formats an item for bg's target, e.g. idTarget("movie", 12) = "movie 12".
func idTarget(kind string, id int64) string { return fmt.Sprintf("%s %d", kind, id) }
