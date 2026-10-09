package automation

import (
	"context"
	"log/slog"
	"sort"
	"strings"
	"sync"

	"github.com/tristenlammi/arrmada/internal/jobs"
)

// The movie search queue: every movie search a person or an event starts — the Search
// button, search on add, a profile change, Re-grab, Block and search again, a request
// approval, a review's re-search, Wanted's Search all — goes through EnqueueMovieSearch.
//
// It is not a second runner. The work is a job on the job runner, in the indexer-search
// class, which runs two searches at a time; the rest wait their turn, and each one's
// timeout starts when it leaves the queue (jobs.Spec.Timeout), so a bulk profile change
// over 300 films no longer times out searches that were only waiting. What this adds on
// top of the runner is the movie view of that queue:
//
//   - Busy(id): the scheduled sweeps leave a movie alone while a search for it is queued
//     or running, instead of searching it a second time;
//   - a position for each new search ("Queued (position 4)") and the running and waiting
//     counts, for GET /api/v1/movies/search-queue and the Movies header;
//   - movie.search.queued / .started / .done on the event bus (staff-only topics), each
//     carrying the counts, so the header stays live without polling.
//
// In memory on purpose, like the runner's own queue: after a restart nothing is waiting
// any more, and the missing and upgrade sweeps cover the movies again on their schedule.

// Topics the queue publishes. Staff-only by the websocket policy's default.
const (
	TopicMovieSearchQueued  = "movie.search.queued"
	TopicMovieSearchStarted = "movie.search.started"
	TopicMovieSearchDone    = "movie.search.done"
)

// MovieQueued is what queuing one search came to.
type MovieQueued struct {
	JobID int64 `json:"job_id,omitempty"`
	// Existing: a search of this kind for this movie was already queued or running, and
	// nothing new was added.
	Existing bool `json:"existing"`
	// Position is the search's place among the movie searches waiting to start (1 = next);
	// 0 when it is running.
	Position int `json:"position"`
}

// MovieQueueEntry is one movie search waiting or running, for the queue view.
type MovieQueueEntry struct {
	ID    int64  `json:"id"`
	Title string `json:"title"`
	Kind  string `json:"kind"` // search | upgrade | regrab
}

// MovieQueueState is the queue right now, oldest first.
type MovieQueueState struct {
	Running []MovieQueueEntry `json:"running"`
	Queued  []MovieQueueEntry `json:"queued"`
}

type movieQueue struct {
	mu      sync.Mutex
	seq     int64
	pending map[int64]map[string]*mqEntry // movie id → job kind → entry
}

type mqEntry struct {
	movieID   int64
	job       string // the job kind: movie.search, movie.upgrade, movie.regrab
	title     string
	seq       int64
	jobID     int64
	submitted bool // handed to the runner; until then a concurrent duplicate submits too
	running   bool
}

// queueKind is the job kind as the queue view names it: "movie.upgrade" → "upgrade".
func queueKind(job string) string { return strings.TrimPrefix(job, "movie.") }

// EnqueueMovieSearch queues spec, a search job for movie id (its Kind one of movie.search,
// movie.upgrade, movie.regrab and its Target "movie:<id>"), through sub — the job runner;
// nil runs it untracked on a goroutine under ctx. A second search of the same kind for a
// movie already queued or running joins it (Existing). The error is the runner's refusal
// (shutting down, or the jobs table can't be written).
func (c *Coordinator) EnqueueMovieSearch(ctx context.Context, sub jobs.Submitter, id int64, spec jobs.Spec) (MovieQueued, error) {
	if c == nil {
		jobID, existing, err := jobs.Start(ctx, sub, slog.Default(), spec)
		return MovieQueued{JobID: jobID, Existing: existing, Position: 1}, err
	}
	q := &c.mq
	q.mu.Lock()
	e := q.pending[id][spec.Kind]
	if e != nil && e.submitted {
		out := MovieQueued{JobID: e.jobID, Existing: true, Position: q.positionLocked(e)}
		q.mu.Unlock()
		return out, nil
	}
	// A search being submitted by a concurrent request (no job id yet): submit too, and
	// the runner's single-flight hands back that job.
	joining := e != nil
	if !joining {
		q.seq++
		e = &mqEntry{movieID: id, job: spec.Kind, seq: q.seq}
		if q.pending == nil {
			q.pending = map[int64]map[string]*mqEntry{}
		}
		if q.pending[id] == nil {
			q.pending[id] = map[string]*mqEntry{}
		}
		q.pending[id][spec.Kind] = e
		c.publishQueueLocked(TopicMovieSearchQueued, e, true)
	}
	q.mu.Unlock()
	if !joining && c.movies != nil {
		// For the queue view; a movie that can't be read still gets its search, which
		// says why it failed.
		if m, err := c.movies.Get(ctx, id); err == nil {
			q.mu.Lock()
			e.title = m.Title
			q.mu.Unlock()
		}
	}

	inner, abandon := spec.Fn, spec.Abandon
	spec.Fn = func(ctx context.Context, p *jobs.Progress) (any, error) {
		c.mqStarted(e)
		defer c.mqDone(e)
		return inner(ctx, p)
	}
	spec.Abandon = func() {
		c.mqDone(e)
		if abandon != nil {
			abandon()
		}
	}
	log := c.log
	if log == nil {
		log = slog.Default()
	}
	jobID, existing, err := jobs.Start(ctx, sub, log, spec)
	switch {
	case joining:
		return MovieQueued{JobID: jobID, Existing: true, Position: c.mqPosition(e)}, err
	case err != nil:
		c.mqDone(e)
		return MovieQueued{}, err
	case existing:
		// The same job was submitted outside the queue; this entry will never run.
		c.mqDone(e)
		return MovieQueued{JobID: jobID, Existing: true, Position: 1}, nil
	}
	q.mu.Lock()
	e.jobID, e.submitted = jobID, true
	out := MovieQueued{JobID: jobID, Position: q.positionLocked(e)}
	q.mu.Unlock()
	return out, nil
}

// MovieSearchBusy reports whether a search for movie id is queued or running through the
// queue. The sweeps skip such a movie: the queued search covers it.
func (c *Coordinator) MovieSearchBusy(id int64) bool {
	if c == nil {
		return false
	}
	c.mq.mu.Lock()
	defer c.mq.mu.Unlock()
	return len(c.mq.pending[id]) > 0
}

// MovieSearchQueue lists the movie searches running and waiting, oldest first.
func (c *Coordinator) MovieSearchQueue() MovieQueueState {
	st := MovieQueueState{Running: []MovieQueueEntry{}, Queued: []MovieQueueEntry{}}
	if c == nil {
		return st
	}
	c.mq.mu.Lock()
	var all []*mqEntry
	for _, byKind := range c.mq.pending {
		for _, e := range byKind {
			all = append(all, e)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].seq < all[j].seq })
	for _, e := range all {
		row := MovieQueueEntry{ID: e.movieID, Title: e.title, Kind: queueKind(e.job)}
		if e.running {
			st.Running = append(st.Running, row)
		} else {
			st.Queued = append(st.Queued, row)
		}
	}
	c.mq.mu.Unlock()
	return st
}

func (c *Coordinator) mqStarted(e *mqEntry) {
	c.mq.mu.Lock()
	defer c.mq.mu.Unlock()
	if c.mq.pending[e.movieID][e.job] != e {
		return
	}
	e.running = true
	c.publishQueueLocked(TopicMovieSearchStarted, e, false)
}

// mqDone lets go of an entry (finished, abandoned, or never submitted). Safe to call twice.
func (c *Coordinator) mqDone(e *mqEntry) {
	c.mq.mu.Lock()
	defer c.mq.mu.Unlock()
	byKind := c.mq.pending[e.movieID]
	if byKind[e.job] != e {
		return
	}
	delete(byKind, e.job)
	if len(byKind) == 0 {
		delete(c.mq.pending, e.movieID)
	}
	c.publishQueueLocked(TopicMovieSearchDone, e, false)
}

func (c *Coordinator) mqPosition(e *mqEntry) int {
	c.mq.mu.Lock()
	defer c.mq.mu.Unlock()
	return c.mq.positionLocked(e)
}

// positionLocked is e's place among the searches waiting (1 = next); 0 when running.
func (q *movieQueue) positionLocked(e *mqEntry) int {
	if e.running {
		return 0
	}
	pos := 1
	for _, byKind := range q.pending {
		for _, o := range byKind {
			if !o.running && o.seq < e.seq {
				pos++
			}
		}
	}
	return pos
}

// countsLocked is how many movie searches are running and waiting.
func (q *movieQueue) countsLocked() (running, queued int) {
	for _, byKind := range q.pending {
		for _, o := range byKind {
			if o.running {
				running++
			} else {
				queued++
			}
		}
	}
	return running, queued
}

// publishQueueLocked announces a change with the counts after it. Published under the
// queue's lock so the events leave in the order the counts changed (the bus never blocks);
// the header can then simply show the latest one.
func (c *Coordinator) publishQueueLocked(topic string, e *mqEntry, withPosition bool) {
	if c.bus == nil {
		return
	}
	running, queued := c.mq.countsLocked()
	data := map[string]any{"id": e.movieID, "kind": queueKind(e.job), "depth": queued, "running": running}
	if withPosition {
		data["position"] = c.mq.positionLocked(e)
	}
	c.bus.Publish(topic, data)
}
