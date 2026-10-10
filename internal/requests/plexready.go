package requests

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// 'Ready' means watchable in Plex. With a Plex server set up, a movie or series request
// whose files are complete isn't announced at once: Plex still has to scan them in, and a
// family member opening Plex straight away would find nothing. The request is stamped
// on_disk_at instead, reads 'Adding to Plex…', and its notice goes out once the Plex
// library index (insights) shows the title — "ready to watch on Plex", with a Watch on
// Plex link — or, if Plex never shows it, after plexGrace with wording that says it may
// take a few more minutes. Books are never in Plex and are never held.
//
// Everything that decides 'ready' (the import's outbox row, the ready sweep, and the Plex
// check that runs every two minutes and after each index rebuild) goes through the same
// gate, notifyReady. The wait lives in the request row, so a restart neither loses it nor
// restarts the clock, and the inbox's unique reference still tells each person once.

const (
	// plexGrace is how long a complete request waits for Plex before it is announced
	// anyway. It fires from the Plex check whether or not the index ever builds.
	plexGrace = 30 * time.Minute
	// plexSettle: the index can only say a show is in Plex, not which episodes, so for a
	// show Plex already had (an earlier season) it counts as there only in an index built
	// at least this long after the files landed — by then Arrmada's partial scan has gone
	// out and the index has been read again (~90 s after the scan).
	plexSettle = 2 * time.Minute
	// plexRelook is how often a waiting request asks for the index to be read again when
	// the last read didn't have it (Plex's own watcher, a slow scan).
	plexRelook = 5 * time.Minute
)

// PlexIDs are the outside ids a title is looked up in Plex by. Zero / "" = unknown.
type PlexIDs struct {
	TMDB int
	TVDB int
	IMDB string
}

// PlexLocator says whether the owner's Plex has a title, from the Plex library index in
// memory (insights; main adapts it). nil-safe: without one, 'ready' goes out on import.
type PlexLocator interface {
	// Configured reports whether a Plex server is set up — whether 'ready' waits for it.
	Configured(ctx context.Context) bool
	// Find looks the title up ("movie" or "series") and answers its Watch on Plex link.
	// It never calls Plex; false while the index isn't built.
	Find(ctx context.Context, media string, ids PlexIDs) (watchURL string, found bool)
	// IndexBuiltAt is when the index was last read from Plex (zero = not yet).
	IndexBuiltAt() time.Time
	// Refresh asks for the index to be read again after the delay (bursts coalesce).
	Refresh(after time.Duration)
}

// SetPlexLocator makes 'ready' wait for Plex (optional).
func (s *Service) SetPlexLocator(p PlexLocator) { s.plex = p }

// plexWaiter is the Plex check's own state: kick wakes RunPlexChecks after an index
// rebuild, and mu keeps the timer's check and a kicked one from overlapping.
type plexWaiter struct {
	once sync.Once
	kick chan struct{}
	mu   sync.Mutex
}

func (w *plexWaiter) ch() chan struct{} {
	w.once.Do(func() { w.kick = make(chan struct{}, 1) })
	return w.kick
}

// plexVerdict is what the gate says about a complete request.
type plexVerdict int

const (
	plexOff  plexVerdict = iota // no Plex (or a book): tell them now, as always
	plexHas                     // Plex has it: "ready to watch on Plex"
	plexLate                    // Plex hasn't shown it within the grace period: tell them anyway
	plexWait                    // not yet
)

// plexGated reports whether 'ready' for this request waits for Plex.
func (s *Service) plexGated(ctx context.Context, req Request) bool {
	return (req.MediaType == "movie" || req.MediaType == "series") && s.plex != nil && s.plex.Configured(ctx)
}

// plexGate decides whether a complete movie or series request can be announced, stamping
// on_disk_at the first time it is asked. An error (the stamp couldn't be written) leaves
// it to the caller's retry.
func (s *Service) plexGate(ctx context.Context, req Request) (plexVerdict, string, error) {
	if !s.plexGated(ctx, req) {
		return plexOff, "", nil
	}
	since := req.onDiskAt
	first := false
	if since == 0 {
		now := s.clock().Unix()
		at, err := s.repo.MarkOnDisk(ctx, req.ID, now)
		if err != nil {
			return plexWait, "", fmt.Errorf("record that request %d is waiting for Plex: %w", req.ID, err)
		}
		// A copy of the row read before another path stamped it finds that stamp instead.
		since, first = at, at == now
	}
	look := s.plexLook(ctx, req.MediaType, req.TMDBID)
	v := s.plexJudge(look, time.Unix(since, 0))
	switch v {
	case plexWait:
		if first {
			// Info, with the ids: a title that never matches waits the whole grace period,
			// and this is how a GUID mismatch gets diagnosed.
			s.log.Info("request-ready: waiting for Plex to show the title", "request", req.ID,
				"tmdb", look.ids.TMDB, "tvdb", look.ids.TVDB, "imdb", look.ids.IMDB, "in_plex", look.found)
			// Open pages swap 'Ready' for 'Adding to Plex…'.
			s.publishUpdated(req, req.Status, s.parties(ctx, req))
		}
	case plexLate:
		s.log.Warn("request-ready: Plex never showed the title; telling the requester anyway", "request", req.ID,
			"tmdb", look.ids.TMDB, "tvdb", look.ids.TVDB, "imdb", look.ids.IMDB, "waited", s.clock().Sub(time.Unix(since, 0)).Round(time.Minute))
	}
	return v, look.url, nil
}

// plexLookup is one title's answer from the index.
type plexLookup struct {
	movie bool
	ids   PlexIDs
	url   string
	found bool
	built time.Time
}

// plexLook finds a title in Plex: by its TMDB id, then — legacy-agent libraries match on
// TVDB and IMDb ids only — with the ids its library record knows.
func (s *Service) plexLook(ctx context.Context, media string, tmdbID int) plexLookup {
	l := plexLookup{movie: media == "movie", ids: PlexIDs{TMDB: tmdbID}, built: s.plex.IndexBuiltAt()}
	if l.url, l.found = s.plex.Find(ctx, media, l.ids); l.found || l.built.IsZero() {
		return l
	}
	more := s.libraryIDs(ctx, media, tmdbID)
	if more.TVDB == 0 && more.IMDB == "" {
		return l
	}
	l.ids = more
	l.url, l.found = s.plex.Find(ctx, media, more)
	return l
}

// libraryIDs is a title's outside ids from its library record (just the TMDB id when it
// can't be read).
func (s *Service) libraryIDs(ctx context.Context, media string, tmdbID int) PlexIDs {
	ids := PlexIDs{TMDB: tmdbID}
	switch media {
	case "movie":
		if s.movies != nil {
			if m, err := s.movies.GetByTMDB(ctx, tmdbID); err == nil {
				ids.IMDB = m.IMDBID
			}
		}
	case "series":
		if s.series != nil {
			if sr, err := s.series.GetByTMDB(ctx, tmdbID); err == nil {
				ids.TVDB, ids.IMDB = sr.TVDBID, sr.IMDBID
			}
		}
	}
	return ids
}

// plexJudge is the rule, for files complete since since: a movie Plex has is there; a
// show only in an index read plexSettle after the files landed (the index can't tell
// seasons apart, and the show may be in Plex from an earlier season). Past plexGrace it
// goes out regardless. While it waits, it makes sure an index read is coming.
func (s *Service) plexJudge(l plexLookup, since time.Time) plexVerdict {
	now := s.clock()
	settled := since.Add(plexSettle)
	if l.found && (l.movie || !l.built.Before(settled)) {
		return plexHas
	}
	if now.Sub(since) >= plexGrace {
		return plexLate
	}
	switch {
	case l.built.Before(settled):
		// The next read must come after the scan Arrmada sent has had its time.
		s.plex.Refresh(max(settled.Sub(now), 0) + 5*time.Second)
	case now.Sub(l.built) >= plexRelook:
		s.plex.Refresh(0)
	}
	return plexWait
}

// readyBody is a request's 'ready' wording: a book is ready to read; a movie or show is
// ready to watch — on Plex once Plex has it, or with a word of warning when Plex hasn't
// shown it within the grace period.
func readyBody(req Request, v plexVerdict) string {
	if req.MediaType == "book" {
		return fmt.Sprintf("“%s” is ready to read.", req.Title)
	}
	return readyWords(requestedWhat(req), v)
}

// readyWords finishes "<what> is ready …" for a verdict.
func readyWords(what string, v plexVerdict) string {
	switch v {
	case plexHas:
		return what + " is ready to watch on Plex."
	case plexLate:
		return what + " is ready — it may take a few more minutes to show up in Plex."
	}
	return what + " is ready to watch."
}

// seasonGate is plexGate for the per-season notices of one request for several seasons,
// each season with its own wait (season_disk_at). The show is looked up once.
type seasonGate struct {
	s      *Service
	req    Request
	gated  bool
	stamps map[int]int64 // season → first seen complete, or seasonTold
	look   *plexLookup
}

func (s *Service) newSeasonGate(ctx context.Context, req Request) *seasonGate {
	return &seasonGate{s: s, req: req, gated: s.plexGated(ctx, req), stamps: decodeSeasonDisk(req.seasonDisk)}
}

// judge is the verdict for complete season n. A season already announced isn't held
// again (a follower who joined since still hears, as before).
func (g *seasonGate) judge(ctx context.Context, n int) (plexVerdict, string, error) {
	since, stamped := g.stamps[n]
	if !g.gated || since == seasonTold {
		return plexOff, "", nil
	}
	if !stamped {
		st, err := g.s.repo.MarkSeasonOnDisk(ctx, g.req.ID, n, g.s.clock().Unix())
		if err != nil {
			return plexWait, "", fmt.Errorf("record that season %d of request %d is waiting for Plex: %w", n, g.req.ID, err)
		}
		g.stamps = st
		if since = st[n]; since <= 0 {
			return plexOff, "", nil // announced meanwhile
		}
	}
	if g.look == nil {
		l := g.s.plexLook(ctx, "series", g.req.TMDBID)
		g.look = &l
	}
	v := g.s.plexJudge(*g.look, time.Unix(since, 0))
	if v == plexLate {
		g.s.log.Warn("request-ready: Plex never showed the season; telling the requester anyway", "request", g.req.ID, "season", n,
			"tmdb", g.look.ids.TMDB, "tvdb", g.look.ids.TVDB, "imdb", g.look.ids.IMDB)
	}
	return v, g.look.url, nil
}

// told marks season n announced, so the Plex check stops looking at it. A season that
// never waited (no Plex) has nothing to mark.
func (g *seasonGate) told(ctx context.Context, n int) {
	if since, stamped := g.stamps[n]; !stamped || since == seasonTold {
		return
	}
	if err := g.s.repo.MarkSeasonTold(ctx, g.req.ID, n); err != nil {
		g.s.log.Warn("request-ready: couldn't record that a season was announced", "request", g.req.ID, "season", n, "err", err)
		return
	}
	g.stamps[n] = seasonTold
}

// KickPlexCheck asks for the Plex check to run now — main calls it after each Plex index
// rebuild. It never blocks (the index worker calls it).
func (s *Service) KickPlexCheck() {
	select {
	case s.plexWaiter.ch() <- struct{}{}:
	default:
	}
}

// RunPlexChecks runs the Plex check each time it is kicked, until ctx ends. The two-minute
// timer is the scheduler's (request-plex-check); this is the "the index just changed"
// path, so a title Plex has just added is announced within seconds of the index seeing it.
func (s *Service) RunPlexChecks(ctx context.Context) {
	kick := s.plexWaiter.ch()
	for {
		select {
		case <-ctx.Done():
			return
		case <-kick:
			if err := s.CheckPlexWaiting(ctx); err != nil && ctx.Err() == nil {
				s.log.Warn("request-ready: Plex check failed", "err", err)
			}
		}
	}
}

// CheckPlexWaiting looks again at every request waiting for Plex: each is announced once
// Plex has it or the grace period is over, by the same path an import takes. It reads the
// index from memory and never calls Plex, so it runs whether or not Plex answers — the
// grace fallback can't be held up by a Plex that is down. A request whose files went again
// before anyone was told starts its wait afresh when they're back.
func (s *Service) CheckPlexWaiting(ctx context.Context) error {
	s.plexWaiter.mu.Lock()
	defer s.plexWaiter.mu.Unlock()
	reqs, err := s.repo.ListPlexWaiting(ctx)
	if err != nil || len(reqs) == 0 {
		return err
	}
	return s.readyPass(ctx, reqs, true)
}

// clearGone forgets the waits of a request (and its seasons) that is no longer complete,
// during the Plex check.
func (s *Service) clearGone(ctx context.Context, lk *readyLookup, rq Request) {
	if rq.onDiskAt > 0 && !lk.ready(ctx, s, rq) {
		if err := s.repo.ClearOnDisk(ctx, rq.ID); err != nil {
			s.log.Warn("request-ready: couldn't reset a Plex wait", "request", rq.ID, "err", err)
		}
	}
	if rq.MediaType != "series" || rq.seasonDisk == "" {
		return
	}
	prog, _ := lk.seasons(ctx, s, rq)
	for n, at := range decodeSeasonDisk(rq.seasonDisk) {
		if p, ok := prog[n]; at > 0 && (!ok || !seasonReady(p)) {
			if err := s.repo.ClearSeasonOnDisk(ctx, rq.ID, n); err != nil {
				s.log.Warn("request-ready: couldn't reset a season's Plex wait", "request", rq.ID, "season", n, "err", err)
			}
		}
	}
}
