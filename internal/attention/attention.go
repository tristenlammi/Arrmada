// Package attention answers "what needs me?" in one place: pending requests, imports held
// in Review, downloads that errored or stalled, imports that keep failing, downloads in the
// wrong category, searches that keep coming up empty and health problems.
//
// It is pull-based on purpose. Badges and alerts built on bus events alone would be
// unreliable, because the bus drops events when a subscriber falls behind; instead a
// refresh asks every source for what is true right now, at most every few seconds, and
// keeps the answer in memory. GET /api/v1/attention serves that answer without making a
// single live call, however many tabs poll it.
package attention

import (
	"context"
	"log/slog"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/eventbus"
	"github.com/tristenlammi/arrmada/internal/safego"
)

// Item kinds. An item's Key is "<kind>:<id>" — stable for as long as the problem lasts, so
// a later alerting step can tell a new problem from one it already announced.
const (
	KindRequest  = "request"  // a request waiting for approval
	KindReview   = "review"   // a finished download held in Review
	KindDownload = "download" // a download the client reports as errored
	KindStalled  = "stalled"  // a download with nobody sending it data for an hour or more
	KindImport   = "import"   // a finished download whose import keeps failing
	KindWrongCat = "wrongcat" // a finished TV download in a category Arrmada doesn't import
	KindSearch   = "search"   // titles whose searches keep coming up empty (one aggregate)
	KindHealth   = "health"   // a health check finding
)

// Levels, the same words the health registry uses.
const (
	LevelWarning = "warning"
	LevelError   = "error"
)

// Item is one thing that needs someone.
type Item struct {
	// Key is "<kind>:<id>": request:12, review:4, download:<hash>, stalled:<hash>,
	// import:<hash>, wrongcat:<hash>, search:stuck, health:<finding key>.
	Key    string `json:"key"`
	Kind   string `json:"kind"`
	Level  string `json:"level"`
	Title  string `json:"title"`
	Detail string `json:"detail,omitempty"`
	// Name is what the item is about, short ("Dune.2021.2160p"), for an alert that lists
	// several in one line. Title stands in where it's empty.
	Name string `json:"-"`
	// Link is where it gets dealt with. LinkKey names the web UI's LINKS entry for the
	// same place (the UI resolves it first, so a page that moves is repointed there).
	Link      string `json:"link,omitempty"`
	LinkKey   string `json:"link_key,omitempty"`
	LinkLabel string `json:"link_label,omitempty"`
	// Since is when the problem started (unix ms): the request or review's own time, when
	// a download stalled, when a health finding was first seen — or, for a source that
	// can't say, when this process first saw it.
	Since int64 `json:"since"`
	// Count is how many titles an aggregate item stands for (search:stuck); 0 otherwise.
	Count int `json:"count,omitempty"`
}

// Counts is how much needs attention, by what the sidebar badges show.
type Counts struct {
	Requests     int `json:"requests"`
	Reviews      int `json:"reviews"`
	Downloads    int `json:"downloads"` // errored + stalled
	Imports      int `json:"imports"`   // failing imports + wrong-category downloads
	Searches     int `json:"searches"`  // titles whose searches keep coming up empty
	Health       int `json:"health"`    // health findings, any level
	HealthErrors int `json:"health_errors"`
	Total        int `json:"total"`
}

// Group is every item of one kind, for the Dashboard's one-line-per-kind card.
type Group struct {
	Kind    string   `json:"kind"`
	Level   string   `json:"level"` // the worst level among its items
	Count   int      `json:"count"`
	Title   string   `json:"title"` // "3 requests are waiting for approval"
	Link    string   `json:"link,omitempty"`
	LinkKey string   `json:"link_key,omitempty"`
	Sample  []string `json:"sample,omitempty"` // up to three names, for a tooltip
}

// Snapshot is the latest answer.
type Snapshot struct {
	At     time.Time `json:"at"`
	Counts Counts    `json:"counts"`
	Groups []Group   `json:"groups"`
	Items  []Item    `json:"items"`
}

// MaxItems caps what a snapshot keeps. Counts and groups cover everything; only the item
// list is cut, worst first.
const MaxItems = 200

// Frame is what one refresh reads once and hands to every provider: the shared download
// queue snapshot and the acquisition record keyed by info hash. Providers never call the
// download client themselves.
type Frame struct {
	Now time.Time
	// OK: the queue could be read at all. Whole: every enabled client answered, so a
	// torrent missing from Queue really is gone. Queue is empty when !OK.
	OK, Whole bool
	Queue     []download.Item
	// Acq is the acquisition record by lowercased info hash (nil when unreadable).
	Acq map[string]automation.Acquisition
}

// InQueue reports whether the frame can tell hash is gone: false only when a complete read
// of the clients doesn't list it.
func (f *Frame) InQueue(hash string) bool {
	if !f.OK || !f.Whole {
		return true
	}
	for _, it := range f.Queue {
		if equalHash(it.Hash, hash) {
			return true
		}
	}
	return false
}

// Provider is one source of attention items. Collect must honour ctx; an error keeps the
// provider's previous items rather than making its problems vanish.
type Provider interface {
	Name() string
	Collect(ctx context.Context, f *Frame) ([]Item, error)
}

// FrameFunc reads the frame for one refresh.
type FrameFunc func(ctx context.Context) *Frame

// Publisher is the event bus, as far as attention needs it.
type Publisher interface {
	Publish(topic string, data any)
}

// TopicChanged is published when the counts change. Counts only: every staff client gets
// it, and the titles stay behind the role-gated endpoint.
const TopicChanged = eventbus.TopicAttentionChanged

// Tunables.
const (
	providerTimeout = 5 * time.Second
	frameTimeout    = 5 * time.Second
	// kickDebounce gathers a burst of changes (a bulk approve) into one refresh.
	kickDebounce = time.Second
	// warnEvery throttles a provider's "couldn't read" warning: the refresh runs every 30
	// seconds and a broken source would otherwise say so twice a minute.
	warnEvery = 10 * time.Minute
	// StaleAfter: a snapshot older than this is reported as stale (the refresh task has
	// stopped running, or keeps failing).
	StaleAfter = 2 * time.Minute
)

// Service holds the providers and the latest snapshot.
type Service struct {
	providers []Provider
	frame     FrameFunc
	pub       Publisher
	log       *slog.Logger
	now       func() time.Time

	cur    atomic.Pointer[Snapshot]
	kick   chan struct{}
	alerts Differ // set before the first refresh (SetAlerts); nil = no alerts

	refreshMu  sync.Mutex // one refresh at a time; the fields below are its own
	prev       map[string][]Item
	firstSeen  map[string]int64
	warnedAt   map[string]time.Time
	lastCounts *Counts
}

// New builds the service. frame may be nil (no download queue: the download providers see
// an unreadable one); pub may be nil (no change events).
func New(frame FrameFunc, pub Publisher, log *slog.Logger, providers ...Provider) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{
		providers: providers, frame: frame, pub: pub, log: log, now: time.Now,
		kick: make(chan struct{}, 1), prev: map[string][]Item{}, firstSeen: map[string]int64{},
		warnedAt: map[string]time.Time{},
	}
}

// Differ is told every refresh's full item list (the Alerter).
type Differ interface {
	Diff(ctx context.Context, items []Item) error
}

// SetAlerts has every refresh end by handing its items to d. Call it before the refresh
// task starts.
func (s *Service) SetAlerts(d Differ) { s.alerts = d }

// Current is the latest snapshot; nil until the first refresh finishes (or on a nil
// service).
func (s *Service) Current() *Snapshot {
	if s == nil {
		return nil
	}
	return s.cur.Load()
}

// Kick asks for a refresh soon, without waiting for it: after a request is made or decided,
// or a review is added or resolved, so badges catch up in seconds rather than at the next
// tick. Several kicks in a row make one refresh. Safe on a nil service.
func (s *Service) Kick() {
	if s == nil {
		return
	}
	select {
	case s.kick <- struct{}{}:
	default: // one is already waiting
	}
}

// Run refreshes after each kick, debounced, until ctx is done. The scheduled refresh task
// is the backstop for anything that changes without a kick.
func (s *Service) Run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.kick:
		}
		t := time.NewTimer(kickDebounce)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-t.C:
		}
		select { // a kick that landed during the wait is covered by this refresh
		case <-s.kick:
		default:
		}
		_ = s.Refresh(ctx)
	}
}

// Refresh asks every provider, in parallel and each under its own timeout, and replaces
// the snapshot. A provider that fails keeps its previous items. It never returns an error
// for a provider's failure (the snapshot is still the best answer there is); the error is
// only ctx's.
func (s *Service) Refresh(ctx context.Context) error {
	s.refreshMu.Lock()
	defer s.refreshMu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	now := s.now()
	f := s.readFrame(ctx, now)

	type result struct {
		items []Item
		err   error
	}
	results := make([]result, len(s.providers))
	var wg sync.WaitGroup
	for i, p := range s.providers {
		wg.Add(1)
		safego.Go(s.log, "attention: "+p.Name(), func() {
			defer wg.Done()
			pctx, cancel := context.WithTimeout(ctx, providerTimeout)
			defer cancel()
			done := make(chan result, 1)
			safego.Go(s.log, "attention: "+p.Name()+" collect", func() {
				var r result
				r.err = safego.Call(s.log, "attention: "+p.Name(), func() error {
					var err error
					r.items, err = p.Collect(pctx, f)
					return err
				})
				done <- r
			})
			select {
			case r := <-done:
				results[i] = r
			case <-pctx.Done():
				results[i] = result{err: pctx.Err()}
			}
		})
	}
	wg.Wait()
	if err := ctx.Err(); err != nil {
		return err // shutting down: keep the last snapshot as it was
	}

	var items []Item
	for i, p := range s.providers {
		r := results[i]
		if r.err != nil {
			s.warn(p.Name(), r.err, now)
			items = append(items, s.prev[p.Name()]...)
			continue
		}
		s.prev[p.Name()] = r.items
		items = append(items, r.items...)
	}
	snap, all := s.build(items, now)
	s.cur.Store(snap)
	if s.alerts != nil {
		// Under refreshMu, so diffs never overlap; the whole list, because an item past
		// the snapshot's cap hasn't gone away.
		if err := s.alerts.Diff(ctx, all); err != nil && ctx.Err() == nil {
			if last, ok := s.warnedAt["alerts"]; !ok || now.Sub(last) >= warnEvery {
				s.warnedAt["alerts"] = now
				s.log.Warn("attention: couldn't update Needs-you alerts — trying again next refresh", "err", err)
			}
		}
	}

	if s.lastCounts == nil || *s.lastCounts != snap.Counts {
		c := snap.Counts
		s.lastCounts = &c
		if s.pub != nil {
			s.pub.Publish(TopicChanged, map[string]any{"counts": c})
		}
	}
	return nil
}

func (s *Service) readFrame(ctx context.Context, now time.Time) *Frame {
	if s.frame == nil {
		return &Frame{Now: now}
	}
	fctx, cancel := context.WithTimeout(ctx, frameTimeout)
	defer cancel()
	f := s.frame(fctx)
	if f == nil {
		f = &Frame{}
	}
	f.Now = now
	return f
}

// warn logs a provider's failure, at most once per warnEvery.
func (s *Service) warn(name string, err error, now time.Time) {
	if last, ok := s.warnedAt[name]; ok && now.Sub(last) < warnEvery {
		return
	}
	s.warnedAt[name] = now
	s.log.Warn("attention: couldn't read a source — showing what it said last time", "source", name, "err", err)
}

// build turns the providers' items into a snapshot: first-seen times filled in, sorted
// worst first, counted, grouped and capped. all is the sorted list before the cap.
func (s *Service) build(items []Item, now time.Time) (snap *Snapshot, all []Item) {
	seen := make(map[string]bool, len(items))
	out := make([]Item, 0, len(items))
	for _, it := range items {
		if it.Key == "" || seen[it.Key] {
			continue // two sources naming the same problem: the first one wins
		}
		seen[it.Key] = true
		if it.Level != LevelError {
			it.Level = LevelWarning
		}
		if _, ok := s.firstSeen[it.Key]; !ok {
			s.firstSeen[it.Key] = now.UnixMilli()
		}
		if it.Since <= 0 {
			it.Since = s.firstSeen[it.Key]
		}
		out = append(out, it)
	}
	for k := range s.firstSeen {
		if !seen[k] {
			delete(s.firstSeen, k)
		}
	}
	sortItems(out)

	snap = &Snapshot{At: now, Counts: countItems(out), Groups: groupItems(out), Items: out}
	if len(snap.Items) > MaxItems {
		snap.Items = snap.Items[:MaxItems]
	}
	return snap, out
}

// kindOrder is the order kinds are listed in: what blocks something first.
var kindOrder = map[string]int{
	KindHealth: 0, KindRequest: 1, KindReview: 2, KindImport: 3, KindDownload: 4,
	KindWrongCat: 5, KindStalled: 6, KindSearch: 7,
}

// sortItems: errors first, then by kind, then oldest first.
func sortItems(items []Item) {
	sort.SliceStable(items, func(i, j int) bool {
		a, b := items[i], items[j]
		if (a.Level == LevelError) != (b.Level == LevelError) {
			return a.Level == LevelError
		}
		if kindOrder[a.Kind] != kindOrder[b.Kind] {
			return kindOrder[a.Kind] < kindOrder[b.Kind]
		}
		if a.Since != b.Since {
			return a.Since < b.Since
		}
		return a.Key < b.Key
	})
}

func countItems(items []Item) Counts {
	var c Counts
	for _, it := range items {
		n := 1
		if it.Count > 0 {
			n = it.Count
		}
		switch it.Kind {
		case KindRequest:
			c.Requests += n
		case KindReview:
			c.Reviews += n
		case KindDownload, KindStalled:
			c.Downloads += n
		case KindImport, KindWrongCat:
			c.Imports += n
		case KindSearch:
			c.Searches += n
		case KindHealth:
			c.Health += n
			if it.Level == LevelError {
				c.HealthErrors += n
			}
		}
	}
	c.Total = c.Requests + c.Reviews + c.Downloads + c.Imports + c.Searches + c.Health
	return c
}

// groupItems makes one group per kind, in kind order.
func groupItems(items []Item) []Group {
	byKind := map[string]*Group{}
	var order []string
	for _, it := range items {
		g := byKind[it.Kind]
		if g == nil {
			g = &Group{Kind: it.Kind, Level: LevelWarning, Link: groupLinks[it.Kind].Path, LinkKey: groupLinks[it.Kind].Key}
			if g.Link == "" {
				g.Link, g.LinkKey = it.Link, it.LinkKey
			}
			byKind[it.Kind] = g
			order = append(order, it.Kind)
		}
		n := 1
		if it.Count > 0 {
			n = it.Count
		}
		g.Count += n
		if it.Level == LevelError {
			g.Level = LevelError
		}
		if len(g.Sample) < 3 && it.Count == 0 {
			g.Sample = append(g.Sample, it.Title)
		}
	}
	sort.SliceStable(order, func(i, j int) bool { return kindOrder[order[i]] < kindOrder[order[j]] })
	out := make([]Group, 0, len(order))
	for _, k := range order {
		g := byKind[k]
		g.Title = groupTitle(k, g.Count)
		out = append(out, *g)
	}
	return out
}

// groupTitle is a group's sentence: "3 requests are waiting for approval".
func groupTitle(kind string, n int) string {
	one := n == 1
	pick := func(single, many string) string {
		if one {
			return single
		}
		return many
	}
	num := itoa(n)
	switch kind {
	case KindRequest:
		return num + pick(" request is waiting for approval", " requests are waiting for approval")
	case KindReview:
		return num + pick(" import needs review", " imports need review")
	case KindDownload:
		return num + pick(" download errored", " downloads errored")
	case KindStalled:
		return num + pick(" download has stalled", " downloads have stalled")
	case KindImport:
		return num + pick(" import keeps failing", " imports keep failing")
	case KindWrongCat:
		return num + pick(" finished TV download is in the wrong category", " finished TV downloads are in the wrong category")
	case KindSearch:
		return num + pick(" title still hasn't found a release", " titles still haven't found a release")
	case KindHealth:
		return num + pick(" health problem", " health problems")
	}
	return num + " items"
}
