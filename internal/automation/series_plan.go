package automation

import (
	"context"
	"log/slog"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// planOpts says who a grab plan is for. The zero value is the automatic sweep, whose
// passes are the battle-tested ones; the scoped options only ever narrow them.
type planOpts struct {
	// Scoped: a user's click on one season or episode rather than the sweep. Every
	// multi-season or complete pack must then also be proportionate to what's wanted, and
	// a complete pack must be worth its size — a click on Season 3 must never pull a box set.
	Scoped bool
	// EpisodesOnly: take single- or multi-episode releases and nothing else. An episode
	// click asks for one file; any pack, however small the season, is more than that.
	EpisodesOnly bool
	// AllowPackFallback: a scoped plan may still take an oversized (but proportionate)
	// pack as the last resort when no single episode covers a gap. The sweep always may.
	AllowPackFallback bool
}

// planInput is everything the planner decides from. cover and try are the only ties to
// the outside world, so the tier rules can be tested without an indexer or a client.
type planInput struct {
	eligible      []quality.Evaluation // ranked best-first
	wanted        []epKey
	seriesSeasons map[int]bool
	counts        map[int]int
	ended         bool
	opts          planOpts
	// cover reports which of the still-needed episodes a release would fill.
	cover func(r parser.Release, needed map[epKey]bool) []epKey
	// try grabs a release and reports whether it's now in flight (freshly grabbed, taken
	// earlier this pass, or still pending from an earlier one). Only then are its episodes
	// counted as covered.
	try func(name, label string) bool

	log   *slog.Logger // optional
	title string       // for the log
}

// planSeriesGrabs chooses which releases fill the wanted episodes, tier by tier, and
// returns what it couldn't cover (sorted). It's the missing sweep's selection, shared with
// the quick Grab/Replace buttons so a click obeys the same pack rules the sweep does.
func planSeriesGrabs(in planInput) []epKey {
	needed := setOf(in.wanted)
	if len(needed) == 0 {
		return nil
	}
	log := in.log
	if log == nil {
		log = slog.New(slog.DiscardHandler)
	}
	// failed marks releases this pass tried and couldn't grab (indexer error, disk guard).
	// The pack passes must both skip them when re-selecting AND not treat their episodes as
	// covered — a transient error on the best pack used to delete its episodes from
	// `needed`, hiding them from every fallback pass and the anime follow-up, then
	// recording a "miss" that grew the backoff up to 12h. Kept here rather than by the
	// caller so a pack that can't be grabbed for any reason can't be re-selected forever.
	failed := map[string]bool{}
	try := func(name, label string) bool {
		if in.try(name, label) {
			return true
		}
		failed[name] = true
		return false
	}
	scoped := in.opts.Scoped
	packsOK := !scoped || !in.opts.EpisodesOnly

	// A whole-show / multi-season pack only makes sense once the show has actually
	// finished — for a still-running series we stick to single-season packs so each new
	// season is grabbed cleanly as its own release.

	// Pass 1 — a complete-series pack that covers every needed season (ended shows only).
	neededSeasons := seasonsOf(needed)
	if in.ended && packsOK {
		for _, ev := range in.eligible {
			r := ev.Candidate.Release
			if r.Kind() != parser.KindCompleteShow {
				continue
			}
			if !coversAllSeasons(r, neededSeasons, in.seriesSeasons) {
				continue
			}
			// Covering what's needed isn't enough — the pack has to be mostly useful.
			// Otherwise one missing episode pulls down an entire six-season show.
			if !packIsProportionate(neededSeasons, packSeasonsOf(r, in.seriesSeasons)) {
				log.Info("series: skipping complete-series pack — too little of it is needed",
					"series", in.title, "release", r.Title,
					"needed_seasons", len(neededSeasons), "pack_seasons", len(packSeasonsOf(r, in.seriesSeasons)))
				continue
			}
			// A click names one season, so "half the seasons are wanted" can still mean a
			// whole second season downloaded for one gap. Weigh it by episodes as well.
			if scoped && !packIsWorthIt(r, len(needed), in.seriesSeasons, in.counts) {
				continue
			}
			if !try(ev.Candidate.Name, "complete series") {
				continue // this one couldn't be grabbed — try the next complete pack
			}
			// The pack covers everything wanted: nothing is left uncovered. Reporting it
			// as remaining made the anime absolute-number follow-up grab single episodes
			// the pack already contains.
			return nil
		}
	}

	// Pass 2 — packs, greedily taking the one that covers the most still-needed episodes.
	// Ended shows may take multi-season (or leftover complete-show) packs so a multi-season
	// pack beats separate season packs; running shows are restricted to single-season packs.
	//
	// worthItOnly first: prefer packs that are mostly useful. A second lap without that
	// restriction runs later, so a gap is never left unfilled just because the only release
	// covering it happens to be a big pack.
	takePacks := func(worthItOnly bool) {
		for {
			var best *quality.Evaluation
			var bestCover int
			for i := range in.eligible {
				if failed[in.eligible[i].Candidate.Name] {
					continue // couldn't be grabbed this pass — re-selecting it would loop
				}
				r := in.eligible[i].Candidate.Release
				if !isPackTier(r.Kind(), in.ended) {
					continue
				}
				// A click never takes more seasons than it has a use for, even as a last
				// resort: an inefficient grab is the sweep's call to make, not a button's.
				if scoped && !packIsProportionate(seasonsOf(needed), packSeasonsOf(r, in.seriesSeasons)) {
					continue
				}
				n := len(in.cover(r, needed))
				if n == 0 {
					continue
				}
				if worthItOnly && !packIsWorthIt(r, n, in.seriesSeasons, in.counts) {
					continue
				}
				if n > bestCover {
					bestCover, best = n, &in.eligible[i]
				}
			}
			if best == nil || bestCover == 0 {
				return
			}
			if !try(best.Candidate.Name, "pack") {
				continue // grab failed — its episodes stay needed; the failed-set skips it next lap
			}
			for _, k := range in.cover(best.Candidate.Release, needed) {
				delete(needed, k)
			}
		}
	}
	if packsOK {
		takePacks(true)
	}

	// Pass 3 — individual episodes for whatever's left. Cheaper and more targeted than a
	// pack when only a few are missing, which is why the disproportionate packs were held
	// back above.
	for _, ev := range in.eligible {
		if len(needed) == 0 {
			break
		}
		r := ev.Candidate.Release
		if r.Kind() != parser.KindEpisode {
			continue
		}
		covered := in.cover(r, needed)
		if len(covered) == 0 {
			continue
		}
		if !try(ev.Candidate.Name, "episode") {
			continue // grab failed — leave its episodes needed for the next candidate
		}
		for _, k := range covered {
			delete(needed, k)
		}
	}
	// Pass 4 — last resort. Anything still missing had no single-episode release either, so
	// take an oversized pack rather than leave the gap: an inefficient grab beats none.
	if len(needed) > 0 && packsOK && (!scoped || in.opts.AllowPackFallback) {
		takePacks(false)
	}
	return sortedKeys(needed)
}

// seriesGrabber makes the grabs a plan asks for and remembers what it did, so the plan
// can't double-grab and the caller can report it.
type seriesGrabber struct {
	c       *Coordinator
	s       series.Series
	profile string
	byName  map[string]indexer.Release
	pending map[string]bool
	// manual and scope are recorded on each grab: whether the user's say-so lets the import
	// skip the quality gate, and for which episodes. The sweep's grabs are neither.
	manual bool
	scope  GrabScope

	grabbed map[string]bool // by download URL
	// grabbedGB tracks what this pass has already committed, so a series with many missing
	// seasons can't queue past the free space by checking each pack against the same
	// (pre-download) free-space reading.
	grabbedGB   float64
	n           int
	titles      []string
	pendingHits int
}

func (c *Coordinator) newSeriesGrabber(s series.Series, profile string, byName map[string]indexer.Release, pending map[string]bool, manual bool, scope GrabScope) *seriesGrabber {
	return &seriesGrabber{c: c, s: s, profile: profile, byName: byName, pending: pending,
		manual: manual, scope: scope, grabbed: map[string]bool{}}
}

// try grabs one release and returns whether it is now in flight — freshly grabbed, a
// duplicate of one this pass already took, or pending from an earlier sweep. Only then may
// the caller count its episodes as covered.
func (g *seriesGrabber) try(ctx context.Context, name, label string) bool {
	c, s := g.c, g.s
	rel := g.byName[name]
	if rel.DownloadURL == "" {
		return false
	}
	if g.grabbed[rel.DownloadURL] {
		return true // already taken this pass — its episodes are covered
	}
	if g.pending[normTitle(rel.Title)] {
		// Already grabbed and still in flight. Grabbing it again just downloads the same
		// bytes twice and stacks a duplicate torrent in the client.
		c.log.Info("series: skipping grab — already grabbed and still importing",
			"series", s.Title, "release", rel.Title)
		notesFrom(ctx).mark(rel.Title, DropPending)
		g.pendingHits++
		return true
	}
	// Space guard — the movie path has had this; TV (where packs are far bigger) did not.
	if !c.diskOKFor(g.grabbedGB + rel.SizeGB()) {
		c.log.Warn("series: skipping grab — not enough free space in the downloads dir",
			"series", s.Title, "release", rel.Title, "release_gb", rel.SizeGB(), "already_queued_gb", g.grabbedGB)
		return false
	}
	hash, err := c.grabTo(ctx, rel.Indexer, rel.DownloadURL, rel.Title, seriesCategory)
	if err != nil {
		c.log.Warn("series: grab failed", "series", s.Title, "release", rel.Title, "err", err)
		return false
	}
	g.grabbed[rel.DownloadURL] = true
	g.n++
	g.grabbedGB += rel.SizeGB()
	g.titles = append(g.titles, rel.Title)
	c.recordSeriesGrab(ctx, s.ID, rel.Title, rel.Indexer, g.profile, hash)
	// An automatic whole-show grab is the column defaults, so there's nothing to write.
	if g.manual || g.scope != WholeShow {
		c.markGrab(ctx, hash, g.manual, g.scope)
	}
	c.series.AddEvent(ctx, s.ID, "grabbed", label+": "+rel.Title+" · "+rel.Indexer)
	c.log.Info("series: grabbing", "series", s.Title, "release", rel.Title, "tier", label)
	return true
}
