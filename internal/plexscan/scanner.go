package plexscan

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/plex"
)

// Settings keys.
const (
	// KeyEnabled: "Tell Plex to scan after changes" (default on).
	KeyEnabled = "plex_scan_on_import"
	// KeyPathMap: the owner's path mappings, as JSON [{from, to}].
	KeyPathMap = "plex_path_map"
)

// MaxPathMaps bounds how many mappings can be saved: a handful covers any real setup.
const MaxPathMaps = 20

// Client is the part of the Plex client the scanner uses (*plex.Client is one).
type Client interface {
	Libraries(ctx context.Context) ([]plex.Library, error)
	RefreshPath(ctx context.Context, sectionKey, dir string) error
	RefreshSection(ctx context.Context, sectionKey string) error
}

// Settings is where the toggle and the mappings live (*settings.Service is one).
type Settings interface {
	Get(ctx context.Context, key, def string) string
	Set(ctx context.Context, key, value string) error
	GetBool(ctx context.Context, key string, def bool) bool
	SetBool(ctx context.Context, key string, value bool) error
}

// Options wires a Scanner.
type Options struct {
	// Client returns a client for the Plex server as configured right now.
	Client func(ctx context.Context) Client
	// Configured reports whether a Plex server URL and token are saved. Nothing is queued
	// or sent while it's false.
	Configured func(ctx context.Context) bool
	Settings   Settings
	// Roots returns Arrmada's library folder per kind (KindMovie, KindShow), read live.
	Roots func(ctx context.Context) map[string]string
	Log   *slog.Logger
}

// Timings. A season pack lands episode by episode over a few seconds, Convert swaps and
// renames come in bursts, and Plex itself coalesces poorly — so each folder waits for
// things to settle, but never more than a minute in all.
const (
	defaultDebounce   = 15 * time.Second
	defaultMaxWait    = 60 * time.Second
	defaultPerTarget  = 60 * time.Second // one scan per Plex folder per minute at most
	defaultSectionTTL = 10 * time.Minute
	retryAfter        = 2 * time.Minute
	maxAttempts       = 3
	// Past this many waiting folders of one kind (a mass rename), they're folded into one
	// scan of the library folder rather than hundreds of partial ones.
	maxPendingPerKind = 200
)

type pending struct {
	kind, dir   string
	first, due  time.Time
	attempts    int
	reqsCovered int // requests folded into this one, for the log
}

type target struct{ section, path string }

// Status is the last scan Arrmada asked Plex for.
type Status struct {
	At      time.Time `json:"at"`
	Kind    string    `json:"kind,omitempty"`
	Path    string    `json:"path,omitempty"`    // Plex-side folder ("" = the whole section)
	Section string    `json:"section,omitempty"` // section title
	How     string    `json:"how,omitempty"`
	Error   string    `json:"error,omitempty"`
}

// Scanner queues changed folders and asks Plex to scan them, debounced. Request never
// blocks and never fails; everything that can go wrong (Plex down, a bad mapping) is
// logged and shown in the settings view, never passed back to an import.
type Scanner struct {
	client     func(ctx context.Context) Client
	configured func(ctx context.Context) bool
	settings   Settings
	roots      func(ctx context.Context) map[string]string
	log        *slog.Logger

	now        func() time.Time
	exists     func(dir string) bool
	debounce   time.Duration
	maxWait    time.Duration
	perTarget  time.Duration
	sectionTTL time.Duration

	mu        sync.Mutex
	pending   map[string]*pending
	lastRun   map[target]time.Time
	secs      []plex.Library
	secsAt    time.Time
	status    Status
	failing   bool            // the last attempt failed: later failures log quietly
	toldWhole map[string]bool // roots already told "add a path mapping"
	listeners []func(kind, sectionKey, plexPath string)

	wake chan struct{}
}

// New builds a scanner. Run starts its worker.
func New(o Options) *Scanner {
	log := o.Log
	if log == nil {
		log = slog.Default()
	}
	return &Scanner{
		client:     o.Client,
		configured: o.Configured,
		settings:   o.Settings,
		roots:      o.Roots,
		log:        log,
		now:        time.Now,
		exists:     dirExists,
		debounce:   defaultDebounce,
		maxWait:    defaultMaxWait,
		perTarget:  defaultPerTarget,
		sectionTTL: defaultSectionTTL,
		pending:    map[string]*pending{},
		lastRun:    map[target]time.Time{},
		toldWhole:  map[string]bool{},
		wake:       make(chan struct{}, 1),
	}
}

// OnRefreshed registers fn to run after each scan Plex accepted (on the worker; keep it
// quick). sectionKey is the Plex section, plexPath the folder ("" = the whole section).
// Register before Run.
func (s *Scanner) OnRefreshed(fn func(kind, sectionKey, plexPath string)) {
	s.mu.Lock()
	s.listeners = append(s.listeners, fn)
	s.mu.Unlock()
}

// Enabled reports the "Tell Plex to scan after changes" toggle.
func (s *Scanner) Enabled(ctx context.Context) bool {
	return s.settings == nil || s.settings.GetBool(ctx, KeyEnabled, true)
}

// Configured reports whether a Plex server is set up.
func (s *Scanner) Configured(ctx context.Context) bool {
	return s.configured != nil && s.configured(ctx)
}

func (s *Scanner) active(ctx context.Context) bool {
	return s.client != nil && s.Configured(ctx) && s.Enabled(ctx)
}

// PathMaps returns the saved mappings (none when unset or unreadable).
func (s *Scanner) PathMaps(ctx context.Context) []PathMap {
	if s.settings == nil {
		return []PathMap{}
	}
	raw := s.settings.Get(ctx, KeyPathMap, "")
	var maps []PathMap
	if raw == "" || json.Unmarshal([]byte(raw), &maps) != nil {
		return []PathMap{}
	}
	return CleanMaps(maps)
}

// Save stores the toggle and the mappings, after checking the mappings are full paths.
func (s *Scanner) Save(ctx context.Context, enabled bool, maps []PathMap) error {
	if s.settings == nil {
		return errors.New("settings are not available")
	}
	for _, m := range maps {
		f, t := strings.TrimSpace(m.From), strings.TrimSpace(m.To)
		if f == "" && t == "" {
			continue
		}
		if f == "" || t == "" {
			return fmt.Errorf("each mapping needs both an Arrmada folder and a Plex folder")
		}
		if !Absolute(f) || !Absolute(t) {
			return fmt.Errorf("%q → %q: both sides must be full paths, like /movies or D:\\Media\\Movies", f, t)
		}
	}
	clean := CleanMaps(maps)
	if len(clean) > MaxPathMaps {
		return fmt.Errorf("at most %d path mappings", MaxPathMaps)
	}
	b, err := json.Marshal(clean)
	if err != nil {
		return err
	}
	if err := s.settings.Set(ctx, KeyPathMap, string(b)); err != nil {
		return err
	}
	if err := s.settings.SetBool(ctx, KeyEnabled, enabled); err != nil {
		return err
	}
	if !enabled {
		s.mu.Lock()
		s.pending = map[string]*pending{} // switched off: nothing waiting goes out
		s.mu.Unlock()
	}
	return nil
}

// Request asks for a scan of dir, a folder as Arrmada sees it, in a library of kind
// (KindMovie or KindShow). It returns at once: the scan goes out from Run once the folder
// has been quiet for the debounce window. A request inside a folder already waiting is
// covered by it; a request for a parent folder replaces the waiting ones inside it.
// Nothing is queued when Plex isn't set up or scanning is switched off.
func (s *Scanner) Request(kind, dir string) {
	if s == nil || strings.TrimSpace(dir) == "" || (kind != KindMovie && kind != KindShow) {
		return
	}
	// Called from import paths with no context of their own; it only reads settings,
	// which are in memory.
	ctx := context.Background()
	if !s.active(ctx) {
		return
	}
	var root string
	if s.roots != nil {
		root = s.roots(ctx)[kind]
	}
	s.mu.Lock()
	s.add(kind, dir, root, s.now())
	s.mu.Unlock()
	s.nudge()
}

func (s *Scanner) nudge() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func pendingKey(kind, dir string) string { return kind + "\x00" + canon(dir) }

// add queues dir. Caller holds s.mu.
func (s *Scanner) add(kind, dir, root string, now time.Time) {
	settle := func(p *pending) {
		p.reqsCovered++
		p.due = now.Add(s.debounce)
		if limit := p.first.Add(s.maxWait); p.due.After(limit) {
			p.due = limit
		}
	}
	first := now
	covered := 0
	n := 0
	for k, p := range s.pending {
		if p.kind != kind {
			continue
		}
		if _, ok := under(dir, p.dir); ok {
			settle(p) // the same folder, or one inside a folder already waiting
			return
		}
		if _, ok := under(p.dir, dir); ok {
			// A folder inside this one was waiting: this scan covers it.
			if p.first.Before(first) {
				first = p.first
			}
			covered += p.reqsCovered
			delete(s.pending, k)
			continue
		}
		n++
	}
	if n >= maxPendingPerKind && root != "" {
		// A mass change: one scan of the library folder instead of hundreds.
		for k, p := range s.pending {
			if p.kind == kind {
				if p.first.Before(first) {
					first = p.first
				}
				covered += p.reqsCovered
				delete(s.pending, k)
			}
		}
		dir = root
	}
	p := &pending{kind: kind, dir: dir, first: first, reqsCovered: covered + 1}
	p.due = now.Add(s.debounce)
	if limit := first.Add(s.maxWait); p.due.After(limit) {
		p.due = limit
	}
	s.pending[pendingKey(kind, dir)] = p
}

// Run is the worker: it sends each folder's scan once it's due, until ctx ends. Start it
// once.
func (s *Scanner) Run(ctx context.Context) {
	for {
		wait := s.untilNext()
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-s.wake:
			t.Stop()
		case <-t.C:
			s.runDue(ctx)
		}
	}
}

// untilNext is how long until the earliest waiting folder is due (an hour when nothing
// waits; a Request wakes the worker anyway).
func (s *Scanner) untilNext() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.pending) == 0 {
		return time.Hour
	}
	now := s.now()
	var next time.Time
	for _, p := range s.pending {
		if next.IsZero() || p.due.Before(next) {
			next = p.due
		}
	}
	if d := next.Sub(now); d > 0 {
		return d
	}
	return 0
}

// runDue sends every scan that's due.
func (s *Scanner) runDue(ctx context.Context) {
	now := s.now()
	s.mu.Lock()
	var due []*pending
	for k, p := range s.pending {
		if !p.due.After(now) {
			due = append(due, p)
			delete(s.pending, k)
		}
	}
	for t, at := range s.lastRun {
		if now.Sub(at) >= s.perTarget {
			delete(s.lastRun, t)
		}
	}
	s.mu.Unlock()
	if len(due) == 0 {
		return
	}
	if !s.active(ctx) {
		return // switched off or disconnected while these waited
	}
	sort.Slice(due, func(i, j int) bool { return due[i].first.Before(due[j].first) })

	secs, err := s.sections(ctx, false)
	if err != nil {
		for _, p := range due {
			s.retry(p, now)
		}
		s.recordFailure(Status{At: now, Kind: due[0].kind, Error: err.Error()}, "plex: couldn't read the Plex libraries to send a scan", err)
		return
	}
	roots := map[string]string{}
	if s.roots != nil {
		roots = s.roots(ctx)
	}
	maps := s.PathMaps(ctx)
	sent := map[target]bool{}
	for _, p := range due {
		res := Resolve(p.kind, s.existing(p.dir, roots[p.kind]), roots[p.kind], secs, maps)
		if len(res.SectionKeys) == 0 {
			s.logOnce("none:"+p.kind, "plex: no Plex library to scan for this change", "kind", p.kind, "note", res.Note)
			continue
		}
		if res.How == HowSection {
			s.logOnce("whole:"+p.kind+":"+roots[p.kind],
				"plex: scanning the whole Plex library because this folder couldn't be matched to Plex's — add a path mapping in the Plex settings for faster scans",
				"kind", p.kind, "arrmada_folder", roots[p.kind])
		}
		targets := make([]target, 0, len(res.SectionKeys))
		var wait time.Time
		for _, key := range res.SectionKeys {
			t := target{key, res.PlexPath}
			if sent[t] {
				continue
			}
			targets = append(targets, t)
			s.mu.Lock()
			last, ok := s.lastRun[t]
			s.mu.Unlock()
			if ok && now.Sub(last) < s.perTarget && last.Add(s.perTarget).After(wait) {
				wait = last.Add(s.perTarget)
			}
		}
		if !wait.IsZero() {
			// Plex was told about this folder less than a minute ago: wait out the minute,
			// then scan once more so this change isn't lost.
			s.requeue(p, wait)
			continue
		}
		for _, t := range targets {
			sent[t] = true
			title := sectionTitle(secs, t.section)
			st := Status{At: now, Kind: p.kind, Path: t.path, Section: title, How: res.How}
			if err := s.refresh(ctx, t); err != nil {
				if errors.Is(err, errNotFound) {
					s.dropSections() // the section went away: read them again next time
				}
				s.retry(p, now)
				st.Error = err.Error()
				s.recordFailure(st, "plex: scan request failed", err, "section", title, "path", t.path)
				break
			}
			s.mu.Lock()
			s.lastRun[t] = now
			s.status = st
			s.failing = false
			listeners := append([]func(string, string, string){}, s.listeners...)
			s.mu.Unlock()
			s.log.Info("plex: asked Plex to scan", "kind", p.kind, "section", title, "path", displayPath(t.path), "how", res.How, "changes", p.reqsCovered)
			for _, fn := range listeners {
				fn(p.kind, t.section, t.path)
			}
		}
	}
}

// dirExists reports whether dir is still there. Only a definite "doesn't exist" counts
// as gone: an unreadable or sleeping disk is left to Plex to deal with.
func dirExists(dir string) bool {
	_, err := os.Stat(dir)
	return !errors.Is(err, fs.ErrNotExist)
}

// existing is dir, or its nearest parent that still exists when dir itself is gone (a
// deleted movie's folder, a renamed show's old one), but never above the library folder.
// Plex skips a folder that isn't there, so the scan has to name one that is for Plex to
// notice what went missing from it.
func (s *Scanner) existing(dir, root string) string {
	for !s.exists(dir) {
		if _, ok := under(dir, root); !ok || canon(dir) == canon(root) {
			return dir
		}
		up := filepath.Dir(dir)
		if up == dir {
			return dir
		}
		dir = up
	}
	return dir
}

var errNotFound = errors.New("plex returned HTTP 404")

func (s *Scanner) refresh(ctx context.Context, t target) error {
	c := s.client(ctx)
	if c == nil {
		return plex.ErrNotConfigured
	}
	cctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var err error
	if t.path == "" {
		err = c.RefreshSection(cctx, t.section)
	} else {
		err = c.RefreshPath(cctx, t.section, t.path)
	}
	if err != nil && strings.Contains(err.Error(), "HTTP 404") {
		return fmt.Errorf("%w (the library section is gone)", errNotFound)
	}
	return err
}

// retry puts a folder back for another go after a failure, a few times at most: Plex
// restarting shouldn't lose a scan, Plex gone for good shouldn't retry forever.
func (s *Scanner) retry(p *pending, now time.Time) {
	p.attempts++
	if p.attempts >= maxAttempts {
		return
	}
	s.requeue(p, now.Add(retryAfter))
}

func (s *Scanner) requeue(p *pending, due time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := pendingKey(p.kind, p.dir)
	if cur, ok := s.pending[k]; ok {
		// A new request for the folder arrived meanwhile; keep the later of the two.
		if due.After(cur.due) {
			cur.due = due
		}
		cur.reqsCovered += p.reqsCovered
		return
	}
	p.due = due
	p.first = due // the max-wait window starts again from here
	s.pending[k] = p
}

func (s *Scanner) recordFailure(st Status, msg string, err error, attrs ...any) {
	s.mu.Lock()
	s.status = st
	quiet := s.failing
	s.failing = true
	s.mu.Unlock()
	attrs = append(attrs, "err", err)
	if quiet {
		s.log.Debug(msg, attrs...)
		return
	}
	s.log.Warn(msg+" (Arrmada will retry; imports are unaffected)", attrs...)
}

func (s *Scanner) logOnce(key, msg string, attrs ...any) {
	s.mu.Lock()
	told := s.toldWhole[key]
	s.toldWhole[key] = true
	s.mu.Unlock()
	if !told {
		s.log.Info(msg, attrs...)
	}
}

// sections returns Plex's library sections, cached for ten minutes (fresh = read now).
func (s *Scanner) sections(ctx context.Context, fresh bool) ([]plex.Library, error) {
	s.mu.Lock()
	if !fresh && s.secs != nil && s.now().Sub(s.secsAt) < s.sectionTTL {
		secs := s.secs
		s.mu.Unlock()
		return secs, nil
	}
	s.mu.Unlock()
	c := s.client(ctx)
	if c == nil {
		return nil, plex.ErrNotConfigured
	}
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	secs, err := c.Libraries(cctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.secs, s.secsAt = secs, s.now()
	s.mu.Unlock()
	return secs, nil
}

func (s *Scanner) dropSections() {
	s.mu.Lock()
	s.secs = nil
	s.mu.Unlock()
}

func sectionTitle(secs []plex.Library, key string) string {
	for _, s := range secs {
		if s.Key == key {
			return s.Title
		}
	}
	return key
}

func displayPath(p string) string {
	if p == "" {
		return "(whole library)"
	}
	return p
}

// LastScan is the last scan asked for (zero At = none since the app started).
func (s *Scanner) LastScan() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

// Pending is how many folders are waiting to be scanned.
func (s *Scanner) Pending() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pending)
}

// RootView is one Arrmada library folder and where its scans go.
type RootView struct {
	Kind        string `json:"kind"`
	ArrmadaRoot string `json:"arrmada_root"`
	Resolution
}

// View is the settings card's picture of the scanner.
type View struct {
	Enabled    bool       `json:"enabled"`
	Configured bool       `json:"configured"`
	PathMap    []PathMap  `json:"path_map"`
	Roots      []RootView `json:"roots"`
	LastScan   *Status    `json:"last_scan"`
	Pending    int        `json:"pending"`
	// Error is why Plex's libraries couldn't be read (the roots then can't be resolved).
	Error string `json:"error,omitempty"`
}

// View resolves each library folder against Plex as it is now. It reads Plex's sections
// (cached) only when Plex is configured.
func (s *Scanner) View(ctx context.Context) View {
	v := View{Enabled: s.Enabled(ctx), Configured: s.Configured(ctx), PathMap: s.PathMaps(ctx), Roots: []RootView{}, Pending: s.Pending()}
	if last := s.LastScan(); !last.At.IsZero() {
		v.LastScan = &last
	}
	roots := map[string]string{}
	if s.roots != nil {
		roots = s.roots(ctx)
	}
	var secs []plex.Library
	if v.Configured && s.client != nil {
		var err error
		if secs, err = s.sections(ctx, false); err != nil {
			v.Error = err.Error()
		}
	}
	for _, kind := range []string{KindMovie, KindShow} {
		rv := RootView{Kind: kind, ArrmadaRoot: roots[kind]}
		if v.Configured && v.Error == "" && rv.ArrmadaRoot != "" {
			rv.Resolution = Resolve(kind, rv.ArrmadaRoot, rv.ArrmadaRoot, secs, v.PathMap)
		}
		if rv.Sections == nil {
			rv.Sections = []string{}
		}
		v.Roots = append(v.Roots, rv)
	}
	return v
}

// ErrUnavailable means a scan can't be sent: Plex isn't set up, or the kind has no
// library folder.
var ErrUnavailable = errors.New("plex scan unavailable")

// ScanNow resolves kind's library folder against Plex's current sections and, when run
// is set, asks Plex to scan it straight away (the settings card's Test and Scan now).
func (s *Scanner) ScanNow(ctx context.Context, kind string, run bool) (RootView, error) {
	if kind != KindMovie && kind != KindShow {
		return RootView{}, fmt.Errorf("%w: unknown library kind %q", ErrUnavailable, kind)
	}
	if !s.Configured(ctx) || s.client == nil {
		return RootView{}, fmt.Errorf("%w: Plex isn't connected", ErrUnavailable)
	}
	root := ""
	if s.roots != nil {
		root = s.roots(ctx)[kind]
	}
	if root == "" {
		return RootView{}, fmt.Errorf("%w: no %s folder is set", ErrUnavailable, kindNoun(kind))
	}
	secs, err := s.sections(ctx, true)
	if err != nil {
		return RootView{}, err
	}
	rv := RootView{Kind: kind, ArrmadaRoot: root, Resolution: Resolve(kind, root, root, secs, s.PathMaps(ctx))}
	if rv.Sections == nil {
		rv.Sections = []string{}
	}
	if !run || len(rv.SectionKeys) == 0 {
		return rv, nil
	}
	now := s.now()
	for _, key := range rv.SectionKeys {
		t := target{key, rv.PlexPath}
		st := Status{At: now, Kind: kind, Path: t.path, Section: sectionTitle(secs, key), How: rv.How}
		if err := s.refresh(ctx, t); err != nil {
			st.Error = err.Error()
			s.mu.Lock()
			s.status = st
			s.mu.Unlock()
			return rv, err
		}
		s.mu.Lock()
		s.lastRun[t] = now
		s.status = st
		s.failing = false
		listeners := append([]func(string, string, string){}, s.listeners...)
		s.mu.Unlock()
		s.log.Info("plex: asked Plex to scan (Scan now)", "kind", kind, "section", st.Section, "path", displayPath(t.path), "how", rv.How)
		for _, fn := range listeners {
			fn(kind, key, t.path)
		}
	}
	return rv, nil
}
