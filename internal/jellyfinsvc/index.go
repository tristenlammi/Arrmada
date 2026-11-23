package jellyfinsvc

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/jellyfin"
	"github.com/tristenlammi/arrmada/internal/mediaserver"
	"github.com/tristenlammi/arrmada/internal/plexscan"
)

// The Jellyfin library index: every movie and show in Jellyfin's libraries, keyed by the
// TMDB / TVDB / IMDb ids Jellyfin matched them to and by their folders, so Arrmada can
// answer "is this title in Jellyfin, and under which item id?" from memory. It backs the
// Jellyfin Watch links and "ready once Jellyfin has it".
//
// The folders are the fallback J0 found a need for: Jellyfin lists each file of a
// multi-version folder whose names don't group as its own movie, with no provider ids at
// all, so only the folder finds them.
//
// One worker (RunIndex) rebuilds it: at start, every 30 minutes, and after Arrmada asked
// Jellyfin to scan something — no sooner than the server's own wait (it scans a reported
// folder once nothing new was reported for a minute), see ScanSettle. Lookups never wait
// for a rebuild; an empty or expired index just nudges the worker.

const (
	indexTTL     = 30 * time.Minute
	indexRetry   = 5 * time.Minute // after a failed build
	indexTimeout = 3 * time.Minute

	// ScanSettle is how long after the last folder report the index waits before it looks:
	// Jellyfin's library monitor delay (60 s by default, restarted by every report) plus a
	// little for the scan itself.
	ScanSettle = 70 * time.Second
	// LibrarySettle is the wait after a whole library was refreshed, which takes longer.
	LibrarySettle = 3 * time.Minute
	// FollowUp is a second look after a scan, in case Jellyfin took longer than the settle
	// time to add what it was told about (a slow disk, internet metadata lookups):
	// otherwise the next look would be the half-hourly one.
	FollowUp = 4 * time.Minute
)

type index struct {
	serverID    string
	libs        []mediaserver.Library
	movieByTMDB map[int]jellyfin.Item
	movieByIMDB map[string]jellyfin.Item
	showByTMDB  map[int]jellyfin.Item
	showByTVDB  map[int]jellyfin.Item
	showByIMDB  map[string]jellyfin.Item
	// Folders as Jellyfin spells them (plexscan.PathKey): a movie's folder, a show's.
	movieByFolder map[string]jellyfin.Item
	showByFolder  map[string]jellyfin.Item
	byID          map[string]jellyfin.Item
	movies, shows int
	builtAt       time.Time
}

func newIndex(serverID string, libs []mediaserver.Library, at time.Time) *index {
	return &index{
		serverID: serverID, libs: libs, builtAt: at,
		movieByTMDB: map[int]jellyfin.Item{}, movieByIMDB: map[string]jellyfin.Item{},
		showByTMDB: map[int]jellyfin.Item{}, showByTVDB: map[int]jellyfin.Item{}, showByIMDB: map[string]jellyfin.Item{},
		movieByFolder: map[string]jellyfin.Item{}, showByFolder: map[string]jellyfin.Item{},
		byID: map[string]jellyfin.Item{},
	}
}

// add files an item under each of its ids and its folder. The first item to claim one
// keeps it (the same film in two libraries opens the first one's copy).
func (x *index) add(it jellyfin.Item) {
	if it.ID == "" {
		return
	}
	x.byID[it.ID] = it
	putInt := func(m map[int]jellyfin.Item, k int) {
		if _, taken := m[k]; k > 0 && !taken {
			m[k] = it
		}
	}
	putStr := func(m map[string]jellyfin.Item, k string) {
		if _, taken := m[k]; k != "" && !taken {
			m[k] = it
		}
	}
	p := it.ProviderIDs
	switch it.Type {
	case jellyfin.TypeMovie:
		x.movies++
		putInt(x.movieByTMDB, p.TMDB)
		putStr(x.movieByIMDB, p.IMDB)
		// A movie's Path is its (first) file; the folder holds every version. A file
		// straight in a library folder has no folder of its own to be found by.
		if dir := parentDir(it.Path); dir != "" && !x.isLibraryFolder(dir) {
			putStr(x.movieByFolder, plexscan.PathKey(dir))
		}
	case jellyfin.TypeSeries:
		x.shows++
		putInt(x.showByTMDB, p.TMDB)
		putInt(x.showByTVDB, p.TVDB)
		putStr(x.showByIMDB, p.IMDB)
		if it.Path != "" && !x.isLibraryFolder(it.Path) {
			putStr(x.showByFolder, plexscan.PathKey(it.Path))
		}
	}
}

func (x *index) isLibraryFolder(dir string) bool {
	k := plexscan.PathKey(dir)
	for _, l := range x.libs {
		for _, loc := range l.Locations {
			if plexscan.PathKey(loc) == k {
				return true
			}
		}
	}
	return false
}

// parentDir is the folder a server-side file path is in, for either kind of separator
// ("" for a bare name).
func parentDir(p string) string {
	p = strings.TrimRight(p, `/\`)
	i := strings.LastIndexAny(p, `/\`)
	if i <= 0 {
		return ""
	}
	return p[:i]
}

// libraries turns Jellyfin's virtual folders into plexscan's libraries: movies → "movie",
// tvshows → "show". A mixed library (no collection type) can hold either, so it's listed
// as both. Music, books and the rest aren't scanned or indexed.
func libraries(vfs []jellyfin.VirtualFolder) []mediaserver.Library {
	var out []mediaserver.Library
	for _, vf := range vfs {
		lib := mediaserver.Library{Key: vf.ItemID, Title: vf.Name, Locations: append([]string(nil), vf.Locations...)}
		switch strings.ToLower(vf.CollectionType) {
		case "movies":
			lib.Type = plexscan.KindMovie
			out = append(out, lib)
		case "tvshows":
			lib.Type = plexscan.KindShow
			out = append(out, lib)
		case "", "mixed":
			lib.Type = plexscan.KindMovie
			out = append(out, lib)
			lib.Type = plexscan.KindShow
			out = append(out, lib)
		}
	}
	return out
}

// indexSchedule is the index and its rebuild schedule (the Plex index's, for Jellyfin).
type indexSchedule struct {
	mu        sync.RWMutex
	idx       *index
	staleAt   time.Time // a rebuild is due at this time (zero = none asked for)
	followAt  time.Time // and a second look then (zero = none)
	retryAt   time.Time // a failed build waits until then
	failing   bool
	listeners []func()
	wake      chan struct{}
	now       func() time.Time // tests
	build     func(ctx context.Context) (*index, error)
}

func (l *indexSchedule) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// wakeChanLocked is created on first use so the zero value works. Caller holds l.mu.
func (l *indexSchedule) wakeChanLocked() chan struct{} {
	if l.wake == nil {
		l.wake = make(chan struct{}, 1)
	}
	return l.wake
}

func (l *indexSchedule) nudge() {
	l.mu.Lock()
	ch := l.wakeChanLocked()
	l.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// drop forgets the index (a different server or key): nothing is found until the next
// build.
func (l *indexSchedule) drop() {
	l.mu.Lock()
	l.idx = nil
	l.retryAt = time.Time{}
	l.mu.Unlock()
}

// current is the index to read, nudging the worker when there is none yet or it has
// expired (the caller still gets the old one meanwhile).
func (l *indexSchedule) current() *index {
	l.mu.RLock()
	idx := l.idx
	now := l.clock()
	needs := (idx == nil || now.Sub(idx.builtAt) > indexTTL) && !now.Before(l.retryAt)
	l.mu.RUnlock()
	if needs {
		l.nudge()
	}
	return idx
}

// markStale asks for a rebuild after the delay. Repeated asks push it back to the latest
// one, without the Plex index's cap: Jellyfin itself doesn't scan until reports stop
// coming for its monitor delay, so a rebuild before the last ask's delay is over would
// only read the library as it was. The 30-minute rebuild still happens meanwhile.
func (l *indexSchedule) markStale(after time.Duration) {
	l.mu.Lock()
	if at := l.clock().Add(after); at.After(l.staleAt) {
		l.staleAt = at
	}
	ch := l.wakeChanLocked()
	l.mu.Unlock()
	select {
	case ch <- struct{}{}:
	default:
	}
}

// markFollowUp asks for a second look after the delay (the latest ask wins).
func (l *indexSchedule) markFollowUp(after time.Duration) {
	l.mu.Lock()
	if at := l.clock().Add(after); at.After(l.followAt) {
		l.followAt = at
	}
	l.mu.Unlock()
}

// due reports whether a rebuild should run now, and how long to sleep otherwise.
func (l *indexSchedule) due(configured bool) (bool, time.Duration) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	now := l.clock()
	if !configured {
		return false, time.Hour // saving a connection nudges
	}
	if now.Before(l.retryAt) {
		return false, l.retryAt.Sub(now)
	}
	if l.idx == nil {
		return true, 0
	}
	next := l.idx.builtAt.Add(indexTTL)
	for _, at := range []time.Time{l.staleAt, l.followAt} {
		if !at.IsZero() && at.Before(next) {
			next = at
		}
	}
	if !now.Before(next) {
		return true, 0
	}
	return false, next.Sub(now)
}

// rebuild builds a fresh index and swaps it in. A failure keeps the old one.
func (l *indexSchedule) rebuild(ctx context.Context) error {
	l.mu.Lock()
	start := l.clock()
	requested, follow := l.staleAt, l.followAt
	l.mu.Unlock()
	idx, err := l.build(ctx)
	l.mu.Lock()
	if err != nil {
		l.retryAt = l.clock().Add(indexRetry)
		l.mu.Unlock()
		return err
	}
	l.idx = idx
	l.retryAt = time.Time{}
	// An ask this build answered is done; one still in the future (this build ran for
	// the half-hourly look) or made while it ran stays scheduled.
	if l.staleAt.Equal(requested) && !requested.After(start) {
		l.staleAt = time.Time{}
	}
	if l.followAt.Equal(follow) && !follow.After(start) {
		l.followAt = time.Time{}
	}
	listeners := append([]func(){}, l.listeners...)
	l.mu.Unlock()
	for _, fn := range listeners {
		fn()
	}
	return nil
}

// RunIndex keeps the library index current until ctx ends. Start it once.
func (s *Service) RunIndex(ctx context.Context) {
	s.idx.mu.Lock()
	wake := s.idx.wakeChanLocked()
	s.idx.mu.Unlock()
	for {
		ok, wait := s.idx.due(s.Configured(ctx))
		if ok {
			bctx, cancel := context.WithTimeout(ctx, indexTimeout)
			start := time.Now()
			err := s.idx.rebuild(bctx)
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
	s.idx.mu.Lock()
	wasFailing := s.idx.failing
	s.idx.failing = err != nil
	idx := s.idx.idx
	s.idx.mu.Unlock()
	if err != nil {
		if !wasFailing {
			s.log.Warn("jellyfin: couldn't read the Jellyfin libraries for Watch links; retrying in 5 minutes", "err", err)
		}
		return
	}
	if wasFailing {
		s.log.Info("jellyfin: library index is reading again")
	}
	s.log.Debug("jellyfin: library index rebuilt", "movies", idx.movies, "shows", idx.shows, "took", took.Round(time.Millisecond))
}

// buildIndex reads every movie and show. The server id comes from the server being
// listed, every time: the item ids belong to it, so its own id is the one links name.
func (s *Service) buildIndex(ctx context.Context) (*index, error) {
	si, err := s.RefreshIdentity(ctx)
	if err != nil {
		return nil, err
	}
	c := s.Client(ctx)
	vfs, err := c.VirtualFolders(ctx)
	if err != nil {
		return nil, err
	}
	items, err := c.AllItems(ctx, jellyfin.ItemsQuery{
		IncludeItemTypes: []string{jellyfin.TypeMovie, jellyfin.TypeSeries},
		Recursive:        true,
		Fields:           []string{"ProviderIds", "DateCreated", "Path"},
	})
	if err != nil {
		return nil, err
	}
	idx := newIndex(si.ID, libraries(vfs), s.idx.clock())
	for _, it := range items {
		idx.add(it)
	}
	return idx, nil
}

// ---- mediaserver.Server ------------------------------------------------------------------

var _ mediaserver.Server = (*Service)(nil)

// Kind is mediaserver.Jellyfin for an Emby connection too: Emby uses the Jellyfin slot (its
// settings, its recorded plays, its linked accounts). Label tells them apart for people.
func (s *Service) Kind() mediaserver.Kind { return mediaserver.Jellyfin }

// Name is the server's own name, as it last said.
func (s *Service) Name(ctx context.Context) string { return s.settings.Get(ctx, KeyServerName, "") }

// IndexReady reports whether lookups can find anything right now.
func (s *Service) IndexReady(ctx context.Context) bool {
	return s.Configured(ctx) && s.idx.current() != nil
}

// IndexBuiltAt is when the index was last rebuilt (zero = never since start).
func (s *Service) IndexBuiltAt() time.Time {
	s.idx.mu.RLock()
	defer s.idx.mu.RUnlock()
	if s.idx.idx == nil {
		return time.Time{}
	}
	return s.idx.idx.builtAt
}

// IndexStale asks for a rebuild after the delay. A burst of asks costs one rebuild.
func (s *Service) IndexStale(after time.Duration) { s.idx.markStale(after) }

// OnIndexRebuilt registers fn to run after each successful rebuild (on the index worker;
// keep it quick).
func (s *Service) OnIndexRebuilt(fn func()) {
	s.idx.mu.Lock()
	s.idx.listeners = append(s.idx.listeners, fn)
	s.idx.mu.Unlock()
}

func isShow(media string) bool {
	switch media {
	case "series", "show", "tv", "episode":
		return true
	}
	return false
}

// Locate finds a title in Jellyfin from memory: movies by TMDB then IMDb, shows by TMDB,
// TVDB, then IMDb, and either by its folder when no id matched. It never calls Jellyfin;
// with Jellyfin not set up, or the index not built yet, it says not found.
func (s *Service) Locate(ctx context.Context, media string, ids mediaserver.ExternalIDs) (mediaserver.Item, bool) {
	if !s.Configured(ctx) {
		return mediaserver.Item{}, false
	}
	idx := s.idx.current()
	if idx == nil {
		return mediaserver.Item{}, false
	}
	it, ok := idx.byIDs(media, ids)
	if !ok && ids.Folder != "" {
		it, ok = s.locateFolder(ctx, idx, media, ids.Folder)
	}
	if !ok {
		return mediaserver.Item{}, false
	}
	return neutral(it), true
}

func (x *index) byIDs(media string, ids mediaserver.ExternalIDs) (jellyfin.Item, bool) {
	if isShow(media) {
		if it, ok := x.showByTMDB[ids.TMDB]; ok && ids.TMDB > 0 {
			return it, true
		}
		if it, ok := x.showByTVDB[ids.TVDB]; ok && ids.TVDB > 0 {
			return it, true
		}
		if it, ok := x.showByIMDB[ids.IMDB]; ok && ids.IMDB != "" {
			return it, true
		}
		return jellyfin.Item{}, false
	}
	if it, ok := x.movieByTMDB[ids.TMDB]; ok && ids.TMDB > 0 {
		return it, true
	}
	if it, ok := x.movieByIMDB[ids.IMDB]; ok && ids.IMDB != "" {
		return it, true
	}
	return jellyfin.Item{}, false
}

// locateFolder translates Arrmada's folder for the title to Jellyfin's spelling, the way a
// scan of it would be (the folder as is, the path mappings, a guess from matching library
// folder names), and looks it up.
func (s *Service) locateFolder(ctx context.Context, idx *index, media, folder string) (jellyfin.Item, bool) {
	kind, byFolder := plexscan.KindMovie, idx.movieByFolder
	if isShow(media) {
		kind, byFolder = plexscan.KindShow, idx.showByFolder
	}
	// The Arrmada folder the title lives in: the default, or an extra Movies/TV folder.
	var roots []string
	if s.roots != nil {
		if r := s.roots(ctx)[kind]; r != "" {
			roots = append(roots, r)
		}
	}
	if s.extraRoots != nil {
		roots = append(roots, s.extraRoots(ctx)[kind]...)
	}
	root := plexscan.RootFor(roots, folder)
	var maps []plexscan.PathMap
	if s.pathMaps != nil {
		maps = s.pathMaps(ctx)
	}
	if res := plexscan.Resolve(kind, folder, root, idx.libs, maps); res.PlexPath != "" {
		if it, ok := byFolder[plexscan.PathKey(res.PlexPath)]; ok {
			return it, true
		}
	}
	it, ok := byFolder[plexscan.PathKey(folder)]
	return it, ok
}

func neutral(it jellyfin.Item) mediaserver.Item {
	media := "movie"
	if it.Type == jellyfin.TypeSeries {
		media = "series"
	}
	return mediaserver.Item{
		Server: mediaserver.Jellyfin, ID: it.ID, Media: media, Title: it.Name, Year: it.ProductionYear,
		IDs:     mediaserver.ExternalIDs{TMDB: it.ProviderIDs.TMDB, TVDB: it.ProviderIDs.TVDB, IMDB: it.ProviderIDs.IMDB},
		AddedAt: it.DateCreated,
	}
}

// WatchURL is the Jellyfin web app's page for the title, at the public address, or ""
// when Jellyfin isn't set up or doesn't have it (yet). Never carries the key.
func (s *Service) WatchURL(ctx context.Context, media string, ids mediaserver.ExternalIDs) string {
	it, ok := s.Locate(ctx, media, ids)
	if !ok {
		return ""
	}
	idx := s.idx.current()
	if idx == nil {
		return ""
	}
	return jellyfin.WatchURLFor(s.ServerKind(ctx), s.PublicURL(ctx), idx.serverID, it.ID)
}

// RecentlyAddedTMDB is Jellyfin's newest movies and episodes mapped to TMDB titles: an
// episode counts as its show; anything without a TMDB id is dropped. Newest first, one
// entry per title. Nothing when the index isn't built.
func (s *Service) RecentlyAddedTMDB(ctx context.Context, limit int) ([]mediaserver.RecentTMDB, error) {
	idx := s.idx.current()
	if idx == nil || !s.Configured(ctx) {
		return nil, nil
	}
	items, err := s.Client(ctx).RecentlyAdded(ctx, limit, "")
	if err != nil {
		return nil, err
	}
	return mapRecent(items, idx), nil
}

func mapRecent(items []jellyfin.Item, idx *index) []mediaserver.RecentTMDB {
	seen := map[string]bool{}
	var out []mediaserver.RecentTMDB
	for _, it := range items {
		media, tmdb := "", 0
		switch it.Type {
		case jellyfin.TypeMovie:
			media, tmdb = "movie", it.ProviderIDs.TMDB
			if tmdb == 0 {
				tmdb = idx.byID[it.ID].ProviderIDs.TMDB
			}
		case jellyfin.TypeEpisode:
			media, tmdb = "series", idx.byID[it.SeriesID].ProviderIDs.TMDB
		case jellyfin.TypeSeries:
			media, tmdb = "series", it.ProviderIDs.TMDB
		}
		if tmdb == 0 {
			continue
		}
		k := media + ":" + strconv.Itoa(tmdb)
		if seen[k] {
			continue
		}
		seen[k] = true
		var added int64
		if !it.DateCreated.IsZero() {
			added = it.DateCreated.Unix()
		}
		out = append(out, mediaserver.RecentTMDB{Media: media, TMDB: tmdb, AddedAt: added})
	}
	return out
}
