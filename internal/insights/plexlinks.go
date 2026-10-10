package insights

import (
	"context"
	"net/url"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/plex"
)

// The Plex library index: every movie and show in the Plex server's libraries, keyed by
// the TMDB / TVDB / IMDb ids Plex matched them to, so Arrmada can answer "is this title in
// Plex, and under which rating key?" from memory. It backs the Watch on Plex links, the
// detail pages' Watched by line and (later) "ready only once Plex has it".
//
// One worker (RunPlexIndex) rebuilds it: at start, every 30 minutes, and about 90 seconds
// after Arrmada asked Plex to scan something. Lookups never wait for a rebuild — they read
// whatever index there is, and an empty or expired one just nudges the worker — so a slow
// or down Plex can't hold up a page.

const (
	plexIndexTTL      = 30 * time.Minute
	plexIndexRetry    = 5 * time.Minute // after a failed build
	plexIndexMaxDelay = 5 * time.Minute // a steady stream of changes can't put a rebuild off longer
	plexIndexTimeout  = 3 * time.Minute
)

// ExternalIDs are the outside ids a title is known by. Zero / "" = unknown.
type ExternalIDs struct {
	TMDB int
	TVDB int
	IMDB string
}

type plexIndex struct {
	machineID   string
	movieByTMDB map[int]plex.Item
	movieByIMDB map[string]plex.Item
	showByTMDB  map[int]plex.Item
	showByTVDB  map[int]plex.Item
	showByIMDB  map[string]plex.Item
	byKey       map[string]plex.Item
	movies      []plex.Item
	shows       []plex.Item
	builtAt     time.Time
}

// plexLinks is the index and its rebuild schedule. Its zero value is ready to use.
type plexLinks struct {
	mu        sync.RWMutex
	idx       *plexIndex
	staleAt   time.Time // a rebuild is due at this time (zero = none asked for)
	staleFrom time.Time // when that rebuild was first asked for
	retryAt   time.Time // a failed build waits until then
	failing   bool
	listeners []func()
	wake      chan struct{}
	now       func() time.Time // tests
	build     func(ctx context.Context) (*plexIndex, error)
}

func (l *plexLinks) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// wakeChan is created on first use so the zero value works. Caller holds l.mu.
func (l *plexLinks) wakeChanLocked() chan struct{} {
	if l.wake == nil {
		l.wake = make(chan struct{}, 1)
	}
	return l.wake
}

func (l *plexLinks) nudge() {
	l.mu.Lock()
	ch := l.wakeChanLocked()
	l.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// current is the index to read, nudging the worker when there is none yet or it has
// expired (the caller still gets the old one meanwhile).
func (l *plexLinks) current() *plexIndex {
	l.mu.RLock()
	idx := l.idx
	now := l.clock()
	needs := (idx == nil || now.Sub(idx.builtAt) > plexIndexTTL) && !now.Before(l.retryAt)
	l.mu.RUnlock()
	if needs {
		l.nudge()
	}
	return idx
}

// markStale asks for a rebuild after the given delay — after a scan, Plex needs a moment
// to add the files. Repeated asks push it back to the latest one, but no further than
// plexIndexMaxDelay after the first.
func (l *plexLinks) markStale(after time.Duration) {
	l.mu.Lock()
	now := l.clock()
	at := now.Add(after)
	if l.staleAt.IsZero() {
		l.staleFrom = now
		l.staleAt = at
	} else if at.After(l.staleAt) {
		l.staleAt = at
	}
	if limit := l.staleFrom.Add(plexIndexMaxDelay); l.staleAt.After(limit) {
		l.staleAt = limit
	}
	ch := l.wakeChanLocked()
	l.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// due reports whether a rebuild should run now, and how long to sleep otherwise.
func (l *plexLinks) due(configured bool) (bool, time.Duration) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	now := l.clock()
	if !configured {
		return false, time.Hour // a Plex being set up nudges through the first lookup
	}
	if now.Before(l.retryAt) {
		return false, l.retryAt.Sub(now)
	}
	if l.idx == nil {
		return true, 0
	}
	next := l.idx.builtAt.Add(plexIndexTTL)
	if !l.staleAt.IsZero() && l.staleAt.Before(next) {
		next = l.staleAt
	}
	if !now.Before(next) {
		return true, 0
	}
	return false, next.Sub(now)
}

// rebuild builds a fresh index and swaps it in. A failure keeps the old one.
func (l *plexLinks) rebuild(ctx context.Context) error {
	l.mu.Lock()
	requested := l.staleAt
	l.mu.Unlock()
	idx, err := l.build(ctx)
	l.mu.Lock()
	if err != nil {
		l.retryAt = l.clock().Add(plexIndexRetry)
		l.mu.Unlock()
		return err
	}
	l.idx = idx
	l.retryAt = time.Time{}
	if l.staleAt.Equal(requested) {
		l.staleAt, l.staleFrom = time.Time{}, time.Time{} // a newer ask stays scheduled
	}
	listeners := append([]func(){}, l.listeners...)
	l.mu.Unlock()
	for _, fn := range listeners {
		fn()
	}
	return nil
}

// RunPlexIndex keeps the Plex library index current until ctx ends. Start it once.
func (s *Service) RunPlexIndex(ctx context.Context) {
	s.links.mu.Lock()
	if s.links.build == nil {
		s.links.build = s.buildPlexIndex
	}
	wake := s.links.wakeChanLocked()
	s.links.mu.Unlock()
	for {
		ok, wait := s.links.due(s.Configured(ctx))
		if ok {
			bctx, cancel := context.WithTimeout(ctx, plexIndexTimeout)
			start := time.Now()
			err := s.links.rebuild(bctx)
			cancel()
			if ctx.Err() != nil {
				return
			}
			s.logIndexResult(err, time.Since(start))
			continue
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return
		case <-wake:
			t.Stop()
		case <-t.C:
		}
	}
}

func (s *Service) logIndexResult(err error, took time.Duration) {
	s.links.mu.Lock()
	wasFailing := s.links.failing
	s.links.failing = err != nil
	idx := s.links.idx
	s.links.mu.Unlock()
	if s.log == nil {
		return
	}
	if err != nil {
		if !wasFailing {
			s.log.Warn("plex: couldn't read the Plex libraries for Watch on Plex links; retrying in 5 minutes", "err", err)
		}
		return
	}
	if wasFailing {
		s.log.Info("plex: library index is reading again")
	}
	s.log.Debug("plex: library index rebuilt", "movies", len(idx.movies), "shows", len(idx.shows), "took", took.Round(time.Millisecond))
}

// buildPlexIndex reads every movie and show section in full.
func (s *Service) buildPlexIndex(ctx context.Context) (*plexIndex, error) {
	c := s.client(ctx)
	// The machine id comes from the server being listed, every time, rather than from a
	// saved setting: the rating keys belong to this server, so its own id is the one the
	// links must name, even right after the URL was changed by hand.
	id, err := c.Identity(ctx)
	if err != nil {
		return nil, err
	}
	machine := id.MachineIdentifier
	libs, err := c.Libraries(ctx)
	if err != nil {
		return nil, err
	}
	idx := &plexIndex{
		machineID:   machine,
		movieByTMDB: map[int]plex.Item{}, movieByIMDB: map[string]plex.Item{},
		showByTMDB: map[int]plex.Item{}, showByTVDB: map[int]plex.Item{}, showByIMDB: map[string]plex.Item{},
		byKey: map[string]plex.Item{},
	}
	for _, lib := range libs {
		typ := 0
		switch lib.Type {
		case "movie":
			typ = plex.TypeMovie
		case "show":
			typ = plex.TypeShow
		default:
			continue
		}
		items, err := c.SectionItems(ctx, lib.Key, typ)
		if err != nil {
			return nil, err
		}
		for _, it := range items {
			idx.add(it)
		}
	}
	idx.builtAt = s.links.clock()
	return idx, nil
}

// add files an item under each of its ids. The first item to claim an id keeps it (the
// same film in a 4K and an HD library opens the first section's copy).
func (x *plexIndex) add(it plex.Item) {
	if it.RatingKey == "" {
		return
	}
	x.byKey[it.RatingKey] = it
	putInt := func(m map[int]plex.Item, k int) {
		if _, taken := m[k]; k > 0 && !taken {
			m[k] = it
		}
	}
	putStr := func(m map[string]plex.Item, k string) {
		if _, taken := m[k]; k != "" && !taken {
			m[k] = it
		}
	}
	switch it.Type {
	case "movie":
		x.movies = append(x.movies, it)
		putInt(x.movieByTMDB, it.TMDB)
		putStr(x.movieByIMDB, it.IMDB)
	case "show":
		x.shows = append(x.shows, it)
		putInt(x.showByTMDB, it.TMDB)
		putInt(x.showByTVDB, it.TVDB)
		putStr(x.showByIMDB, it.IMDB)
	}
}

// isShow maps the media words the app uses to Plex's kind.
func isShow(media string) bool {
	switch media {
	case "series", "show", "tv", "episode":
		return true
	}
	return false
}

// Locate finds a title in Plex from memory: movies by TMDB then IMDb, shows by TMDB,
// TVDB, then IMDb (legacy-agent libraries only know the latter). It never calls Plex; with
// Plex not set up, or the index not built yet, it says not found.
func (s *Service) Locate(ctx context.Context, media string, ids ExternalIDs) (plex.Item, bool) {
	if !s.Configured(ctx) {
		return plex.Item{}, false
	}
	idx := s.links.current()
	if idx == nil {
		return plex.Item{}, false
	}
	if isShow(media) {
		if it, ok := idx.showByTMDB[ids.TMDB]; ok && ids.TMDB > 0 {
			return it, true
		}
		if it, ok := idx.showByTVDB[ids.TVDB]; ok && ids.TVDB > 0 {
			return it, true
		}
		if it, ok := idx.showByIMDB[ids.IMDB]; ok && ids.IMDB != "" {
			return it, true
		}
		return plex.Item{}, false
	}
	if it, ok := idx.movieByTMDB[ids.TMDB]; ok && ids.TMDB > 0 {
		return it, true
	}
	if it, ok := idx.movieByIMDB[ids.IMDB]; ok && ids.IMDB != "" {
		return it, true
	}
	return plex.Item{}, false
}

// PlexIndexReady reports whether lookups can find anything right now: Plex is set up and
// the index has been built. Callers use it to skip work (reading library records for
// fallback ids) that could only lead to "not found".
func (s *Service) PlexIndexReady(ctx context.Context) bool {
	return s.Configured(ctx) && s.links.current() != nil
}

// WatchURL is the app.plex.tv page that plays the title on the owner's server, or "" when
// Plex isn't set up or doesn't have it (yet). Never contains the token or server address.
func (s *Service) WatchURL(ctx context.Context, media string, ids ExternalIDs) string {
	it, ok := s.Locate(ctx, media, ids)
	if !ok {
		return ""
	}
	idx := s.links.current()
	if idx == nil || idx.machineID == "" {
		return ""
	}
	return watchURL(idx.machineID, it.RatingKey)
}

func watchURL(machineID, ratingKey string) string {
	return "https://app.plex.tv/desktop/#!/server/" + url.PathEscape(machineID) +
		"/details?key=" + url.QueryEscape("/library/metadata/"+ratingKey)
}

// TMDBForRatingKey maps a Plex rating key back to the title's TMDB id ("movie" or
// "series"); ok is false when the index doesn't know the key or Plex has no TMDB id for it.
func (s *Service) TMDBForRatingKey(key string) (media string, tmdb int, ok bool) {
	idx := s.links.current()
	if idx == nil {
		return "", 0, false
	}
	it, found := idx.byKey[key]
	if !found || it.TMDB == 0 {
		return "", 0, false
	}
	if it.Type == "show" {
		return "series", it.TMDB, true
	}
	return "movie", it.TMDB, true
}

// PlexItems lists the indexed movies ("movie") or shows ("series"/"show"), a copy.
func (s *Service) PlexItems(kind string) []plex.Item {
	idx := s.links.current()
	if idx == nil {
		return nil
	}
	if isShow(kind) {
		return append([]plex.Item(nil), idx.shows...)
	}
	return append([]plex.Item(nil), idx.movies...)
}

// PlexIndexBuiltAt is when the index was last rebuilt (zero = never since start).
func (s *Service) PlexIndexBuiltAt() time.Time {
	s.links.mu.RLock()
	defer s.links.mu.RUnlock()
	if s.links.idx == nil {
		return time.Time{}
	}
	return s.links.idx.builtAt
}

// PlexIndexStale asks for a rebuild after the given delay: Arrmada just asked Plex to scan
// something (or imported a file Plex's own watcher will pick up), and Plex needs a moment
// to add it. A burst of asks costs one rebuild.
func (s *Service) PlexIndexStale(after time.Duration) {
	s.links.markStale(after)
}

// OnPlexIndexRebuilt registers fn to run after each successful rebuild (on the index
// worker; keep it quick). REQ-style "is it in Plex yet?" checks re-run from here.
func (s *Service) OnPlexIndexRebuilt(fn func()) {
	s.links.mu.Lock()
	s.links.listeners = append(s.links.listeners, fn)
	s.links.mu.Unlock()
}
