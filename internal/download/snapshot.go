package download

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// snapshotTTL is how long one read of the download clients answers for. Every open
// Downloads or Movies tab polls every few seconds, and so do the import sweeps and the
// health check; each used to make its own torrents/info call. Two seconds of staleness on
// a progress bar costs nothing, and an action through this service (pause, remove, add)
// drops the cached copy so the next poll shows it.
const snapshotTTL = 2 * time.Second

// snapshotFetchTimeout bounds one shared read. It runs detached from the caller that
// happened to start it, so a closed browser tab can't fail the read for everyone else
// waiting on it — but a hung client must still let them go.
const snapshotFetchTimeout = 30 * time.Second

// errSnapshotAborted is what waiters get if the read they were sharing never finished (it
// panicked). It reads as an outage, which is the safe direction: nothing concludes a
// torrent is gone from an error.
var errSnapshotAborted = errors.New("download queue read was aborted")

// ClientHealth is whether one download client answered the last queue read, and since when
// it hasn't. LastOK survives a failure; Since is the start of the current outage and is
// kept across later failures, so "unreachable since 14:02" doesn't creep forward.
type ClientHealth struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Kind      Kind      `json:"kind"`
	Enabled   bool      `json:"enabled"`
	Reachable bool      `json:"reachable"`
	LastOK    time.Time `json:"last_ok,omitzero"`
	Since     time.Time `json:"since,omitzero"` // first failure of the current outage; zero while reachable
	LastErr   string    `json:"error,omitempty"`
	LatencyMS int64     `json:"latency_ms"`
}

// Snapshot is one read of every download client's queue.
//
// Complete is false when an enabled client didn't answer: its torrents are missing from
// Items, so nothing may conclude a torrent is gone from its absence. Err is set when no
// client answered at all (or the client list couldn't be read) — an outage, never an empty
// queue.
type Snapshot struct {
	Items    []Item
	Complete bool
	Health   []ClientHealth
	At       time.Time
	Err      error
}

// clone copies the slices, so callers can sort or filter what they're handed without
// racing each other over the cached copy.
func (s Snapshot) clone() Snapshot {
	out := s
	out.Items = append([]Item(nil), s.Items...)
	out.Health = append([]ClientHealth(nil), s.Health...)
	return out
}

// snapshotCache is the shared, single-flight queue read behind Snapshot.
type snapshotCache struct {
	mu  sync.Mutex
	cur *Snapshot // the last finished read
	// gen counts invalidations. A read carries the gen it started under and is only cached
	// if nothing invalidated since: a Pause that lands mid-read must not be hidden by the
	// read that began before it.
	gen      uint64
	curGen   uint64
	inflight *snapshotFetch
	health   map[int64]ClientHealth // each client's LastOK / outage start, across reads
	now      func() time.Time       // nil = time.Now; tests step it
	ttl      time.Duration          // 0 = snapshotTTL
}

type snapshotFetch struct {
	done chan struct{}
	gen  uint64
	snap Snapshot
}

func (c *snapshotCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *snapshotCache) maxAge() time.Duration {
	if c.ttl > 0 {
		return c.ttl
	}
	return snapshotTTL
}

// invalidateSnapshot drops the cached read, so the next caller asks the clients again.
func (s *Service) invalidateSnapshot() {
	s.snap.mu.Lock()
	s.snap.gen++
	s.snap.mu.Unlock()
}

// InvalidateSnapshot makes the next queue read go to the clients rather than the cache.
// Everything that changes a torrent through this service already does it; this is for a
// caller that changed the client some other way.
func (s *Service) InvalidateSnapshot() { s.invalidateSnapshot() }

// Snapshot returns every client's queue, read at most once per two seconds however many
// callers ask, with each client's health. Concurrent callers share one read. The error is
// Snapshot.Err: set only when no client answered.
func (s *Service) Snapshot(ctx context.Context) (Snapshot, error) {
	c := &s.snap
	c.mu.Lock()
	if c.cur != nil && c.curGen == c.gen && c.clock().Sub(c.cur.At) < c.maxAge() {
		snap := c.cur.clone()
		c.mu.Unlock()
		return snap, snap.Err
	}
	f := c.inflight
	leader := false
	if f == nil || f.gen != c.gen {
		// No read running, or only one that started before an invalidation: start ours.
		f = &snapshotFetch{done: make(chan struct{}), gen: c.gen}
		c.inflight = f
		leader = true
	}
	c.mu.Unlock()

	if leader {
		s.runSnapshot(ctx, f)
	}
	select {
	case <-f.done:
		snap := f.snap.clone()
		return snap, snap.Err
	case <-ctx.Done():
		return Snapshot{}, ctx.Err()
	}
}

// runSnapshot performs one shared read and hands it to everyone waiting on f.
func (s *Service) runSnapshot(ctx context.Context, f *snapshotFetch) {
	c := &s.snap
	f.snap = Snapshot{Err: errSnapshotAborted, At: c.clock()}
	defer func() {
		c.mu.Lock()
		if c.inflight == f {
			c.inflight = nil
		}
		if f.snap.Err != errSnapshotAborted && f.gen == c.gen {
			snap := f.snap
			c.cur, c.curGen = &snap, f.gen
		}
		c.mu.Unlock()
		close(f.done)
	}()
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), snapshotFetchTimeout)
	defer cancel()
	f.snap = s.readQueue(rctx)
}

// readQueue asks every client (see existingClients) for its torrents and records each
// one's health.
//
// A partial result — some clients answered, some didn't — comes back with Complete false
// and no error. If every client failed, that is an outage, not an empty queue: returning
// an empty list there makes the import sweep, the in-flight checks and stall detection
// wrongly conclude nothing is downloading.
func (s *Service) readQueue(ctx context.Context) Snapshot {
	c := &s.snap
	snap := Snapshot{At: c.clock()}
	clients, err := s.existingClients(ctx)
	if err != nil {
		snap.Err = err
		return snap
	}
	var listed bool
	var failed int
	var lastErr error
	seen := make(map[int64]bool, len(clients))
	for _, dc := range clients {
		impl, ok := s.registry.For(dc.Kind)
		if !ok {
			continue
		}
		seen[dc.ID] = true
		start := time.Now()
		part, err := impl.List(ctx, dc)
		dur := time.Since(start)
		s.record(ctx, dc, err, dur)
		snap.Health = append(snap.Health, c.noteHealth(dc, err, dur))
		if err != nil {
			lastErr = err
			if !dc.Enabled {
				// Switched off and not answering: most likely stopped on purpose. Its
				// torrents can't be read, but counting it as down would pause stall
				// fail-over for every other client for as long as it stays off.
				s.log.Debug("disabled download client didn't answer", "client", dc.Name, "err", err)
				continue
			}
			s.log.Warn("download client list failed", "client", dc.Name, "err", err)
			failed++
			continue
		}
		listed = true
		for i := range part {
			part[i].ClientID = dc.ID
		}
		snap.Items = append(snap.Items, part...)
	}
	c.pruneHealth(seen)
	if !listed && lastErr != nil {
		snap.Items = nil
		snap.Err = fmt.Errorf("all download clients failed to list: %w", lastErr)
		return snap
	}
	snap.Complete = failed == 0
	return snap
}

// noteHealth folds one client's answer into its running health and returns it.
func (c *snapshotCache) noteHealth(dc Client, err error, dur time.Duration) ClientHealth {
	now := c.clock()
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.health == nil {
		c.health = map[int64]ClientHealth{}
	}
	h := c.health[dc.ID]
	h.ID, h.Name, h.Kind, h.Enabled = dc.ID, dc.Name, dc.Kind, dc.Enabled
	h.LatencyMS = dur.Milliseconds()
	if err == nil {
		h.Reachable, h.LastOK, h.Since, h.LastErr = true, now, time.Time{}, ""
	} else {
		if h.Reachable || h.Since.IsZero() {
			h.Since = now // a new outage starts now; a running one keeps its start
		}
		h.Reachable, h.LastErr = false, err.Error()
	}
	c.health[dc.ID] = h
	return h
}

// pruneHealth forgets clients that no longer exist, so a deleted client's outage doesn't
// linger (and an id SQLite hands to the next client doesn't inherit it).
func (c *snapshotCache) pruneHealth(seen map[int64]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for id := range c.health {
		if !seen[id] {
			delete(c.health, id)
		}
	}
}

// Health is each client's state as of the shared queue read.
func (s *Service) Health(ctx context.Context) ([]ClientHealth, error) {
	snap, err := s.Snapshot(ctx)
	return snap.Health, err
}

// EnabledStates is the health panel's view of a snapshot's health: each enabled client
// and whether it answered the last read. A switched-off client isn't expected to answer.
func EnabledStates(health []ClientHealth) []ClientState {
	out := make([]ClientState, 0, len(health))
	for _, h := range health {
		if !h.Enabled {
			continue
		}
		out = append(out, ClientState{ID: h.ID, Name: h.Name, Kind: h.Kind, OK: h.Reachable, Err: h.LastErr, LatencyMS: h.LatencyMS})
	}
	return out
}
