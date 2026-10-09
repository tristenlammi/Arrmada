package automation

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// ErrSpecialsScope refuses a season-level grab on Specials. Specials aren't released as
// packs, and anything that "covers season 0" is really a pack of the whole show — so the
// only safe Specials grab is one special at a time.
var ErrSpecialsScope = errors.New("Specials have no packs — grab a single special")

// SeriesScope is one user click on a show: Grab missing on a season, Grab on an episode,
// or Replace on an episode that already has a file.
type SeriesScope struct {
	Season, Episode int
	// Replace fetches the episode even though it has a file, and the user's say-so then
	// lets that one episode's new file skip the import gate.
	Replace bool
	// Trigger names the button, for the log.
	Trigger string
}

// Validate refuses a scope no button should send. Cheap, so the API can answer 400 before
// it starts a background search.
func (sc SeriesScope) Validate() error {
	switch {
	case sc.Season < 0 || sc.Episode < 0:
		return errors.New("season and episode can't be negative")
	case sc.Season == 0 && sc.Episode == 0:
		return ErrSpecialsScope
	case sc.Replace && sc.Episode == 0:
		return errors.New("Replace needs an episode")
	}
	return nil
}

func (sc SeriesScope) label() string {
	if sc.Episode > 0 {
		return fmt.Sprintf("S%02dE%02d", sc.Season, sc.Episode)
	}
	return fmt.Sprintf("Season %d", sc.Season)
}

// GrabOutcome is what one user-triggered search found and did. It's written to the
// show's history and published on the bus, so "I clicked Grab and nothing happened" has
// an answer that isn't buried in the logs.
type GrabOutcome struct {
	Scope string `json:"scope"`
	// Searched is false when the click needed no search at all (nothing was missing).
	Searched    bool `json:"searched"`
	Found       int  `json:"found"`
	WrongShow   int  `json:"wrong_show"`
	OutOfScope  int  `json:"out_of_scope"`
	Blocklisted int  `json:"blocklisted"`
	// Pending counts releases the plan wanted that are already downloading.
	Pending  int `json:"pending"`
	Eligible int `json:"eligible"`
	// Rejected counts the profile's reasons, keyed by the first clause of each one so
	// "Over your 20 Mbps ceiling (25.1 Mbps)" and "(31.0 Mbps)" count together.
	Rejected       map[string]int    `json:"rejected,omitempty"`
	Example        map[string]string `json:"example,omitempty"`
	IndexerErrors  int               `json:"indexer_errors"`
	IndexersFailed bool              `json:"indexers_failed"`
	Grabbed        []string          `json:"grabbed,omitempty"`
	Note           string            `json:"note,omitempty"`
}

// reject counts one profile rejection under its reason's first clause.
func (o *GrabOutcome) reject(reason, example string) {
	key := reason
	for _, sep := range []string{" — ", " (", ": "} {
		if i := strings.Index(key, sep); i > 0 {
			key = key[:i]
		}
	}
	if key == "" {
		key = "rejected by the profile"
	}
	if o.Rejected == nil {
		o.Rejected, o.Example = map[string]int{}, map[string]string{}
	}
	o.Rejected[key]++
	if _, ok := o.Example[key]; !ok {
		o.Example[key] = example
	}
}

// Summary is the one-line history entry, e.g.
// "37 found · 0 fit: 22 other shows, 15 Over your 20 Mbps ceiling".
func (o GrabOutcome) Summary() string {
	if o.IndexersFailed {
		return fmt.Sprintf("Every indexer failed (%d) — nothing was searched", o.IndexerErrors)
	}
	if !o.Searched {
		return o.Note
	}
	type reason struct {
		n    int
		what string
	}
	var reasons []reason
	add := func(n int, what string) {
		if n > 0 {
			reasons = append(reasons, reason{n, what})
		}
	}
	add(o.WrongShow, "other shows")
	add(o.OutOfScope, "other episodes")
	add(o.Blocklisted, "blocklisted")
	for k, n := range o.Rejected {
		add(n, k)
	}
	sort.SliceStable(reasons, func(i, j int) bool {
		if reasons[i].n != reasons[j].n {
			return reasons[i].n > reasons[j].n
		}
		return reasons[i].what < reasons[j].what
	})
	line := fmt.Sprintf("%d found · %d fit", o.Found, o.Eligible)
	if len(reasons) > 0 {
		parts := make([]string, 0, len(reasons))
		for _, r := range reasons {
			parts = append(parts, fmt.Sprintf("%d %s", r.n, r.what))
		}
		line += ": " + strings.Join(parts, ", ")
	}
	switch {
	case len(o.Grabbed) > 0:
		line = "Grabbed " + strings.Join(o.Grabbed, ", ") + " · " + line
	case o.Pending > 0:
		line = "Already downloading · " + line
	default:
		line = "Grabbed nothing · " + line
	}
	if o.IndexerErrors > 0 {
		line += fmt.Sprintf(" · %d indexer%s failed", o.IndexerErrors, plural(o.IndexerErrors))
	}
	if o.Note != "" {
		line += " · " + o.Note
	}
	return line
}

// scopeWanted lists the episodes a click should fetch: aired and without a file. A season
// click needs each episode monitored but waives the season's flag, since naming the season
// is the say-so; an episode click waives monitoring entirely. Replace takes its episode
// even though it has a file — replacing that file is the point.
func scopeWanted(s series.Series, sc SeriesScope) []epKey {
	var out []epKey
	for _, sn := range s.Seasons {
		if sn.SeasonNumber != sc.Season {
			continue
		}
		for _, e := range sn.Episodes {
			k := epKey{sn.SeasonNumber, e.EpisodeNumber}
			switch {
			case sc.Episode > 0:
				if e.EpisodeNumber == sc.Episode && (sc.Replace || (!e.HasFile && aired(e.AirDate))) {
					out = append(out, k)
				}
			case e.Monitored && !e.HasFile && aired(e.AirDate):
				out = append(out, k)
			}
		}
	}
	return out
}

// GrabForScope is the quick Grab missing / Grab / Replace action: search for one season or
// episode and grab through the sweep's own tier rules, narrowed for a click. An episode
// never takes a pack; a season takes a pack only when most of it is missing, or as a last
// resort when no single episode exists — and never a box set on a running show.
//
// The outcome is written to the show's history and published as series.searched, whether
// or not anything was grabbed.
func (c *Coordinator) GrabForScope(ctx context.Context, seriesID int64, sc SeriesScope) (out GrabOutcome, err error) {
	if err := sc.Validate(); err != nil {
		return GrabOutcome{}, err
	}
	if c.series == nil {
		return GrabOutcome{}, fmt.Errorf("series module not available")
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return GrabOutcome{}, err
	}
	scope := ScopeFor(sc.Season, sc.Episode)
	out.Scope = scope.String()
	if sc.Replace {
		ctx = WithSearchTrigger(ctx, TriggerReplace)
	} else {
		ctx = WithDefaultSearchTrigger(ctx, TriggerManual)
	}
	ctx, notes := newSearchNotes(ctx)
	// Every click leaves a trace in the history, a failed search included, and a
	// search_attempts row under its season or episode.
	defer func() {
		if err != nil {
			out.Note = "search failed: " + err.Error()
		}
		c.reportSearched(ctx, s, sc, out)
		so, serr := SearchOutcome{Searched: out.Searched}, err
		if out.IndexersFailed {
			// The click answers with its own outcome and no error; the attempt still
			// says nobody could answer.
			serr = &indexer.AllFailedError{Errors: notes.errorMap()}
		}
		c.recordAttempt(ctx, notes, AttemptSeries, seriesID, out.Scope, &so, serr)
	}()

	wanted := scopeWanted(s, sc)
	if len(wanted) == 0 {
		out.Note = "Nothing missing in " + sc.label()
		return out, nil
	}

	releases, ixErrs, serr := c.searchSeriesScope(ctx, s, sc.Season, sc.Episode)
	out.Searched = true
	out.IndexerErrors = len(ixErrs)
	if serr != nil {
		var all *indexer.AllFailedError
		if errors.As(serr, &all) {
			out.IndexersFailed, out.IndexerErrors = true, len(all.Errors)+len(all.Skipped)
			return out, nil
		}
		return out, serr
	}

	blocked, err := c.blockedSetSeries(ctx, s.ID)
	if err != nil {
		c.skipUnreadable(s.Title, err)
		return out, err
	}
	pending, err := c.pendingSeriesGrabTitles(ctx, s.ID)
	if err != nil {
		c.skipUnreadable(s.Title, err)
		return out, err
	}
	byName := make(map[string]indexer.Release, len(releases))
	cands := make([]quality.Candidate, 0, len(releases))
	rts := newRuntimeIndex(s)
	torrents := grabbable(releases)
	notes.dropped(releases, torrents, DropNotTorrent)
	for _, rel := range bestByTitle(torrents) {
		out.Found++
		switch {
		case blocked[normTitle(rel.Title)]:
			out.Blocklisted++
			notes.mark(rel.Title, DropBlocklisted)
		case !seriesTitleMatches(rel.Title, s):
			out.WrongShow++
			notes.mark(rel.Title, DropWrongTitle)
		case !c.releaseMatchesScope(ctx, s, parser.Parse(rel.Title), sc.Season, sc.Episode):
			out.OutOfScope++
			notes.mark(rel.Title, DropOutOfScope)
		default:
			byName[rel.Title] = rel
			cands = append(cands, c.newSeriesCandidate(ctx, s, rts, rel))
		}
	}
	profile := c.effectiveProfile(ctx, s.QualityProfile, quality.MediaSeries)
	decision := c.quality.Decide(ctx, profile, cands)
	notes.decided(decision)
	out.Eligible = len(decision.Eligible)
	for _, ev := range decision.Rejected {
		out.reject(ev.RejectReason, ev.Candidate.Name)
	}

	episodeOnly := sc.Episode > 0
	_, seriesSeasons := wantedEpisodes(s)
	cover := func(r parser.Release, needed map[epKey]bool) []epKey {
		return c.coveredByFor(ctx, s, r, needed)
	}
	if sc.Season == 0 {
		// A special is filled only by a release tagged S00 — never through an alias,
		// absolute or scene mapping, which know nothing of season 0.
		cover = func(r parser.Release, needed map[epKey]bool) []epKey {
			if !isSpecialRelease(r, 0) {
				return nil
			}
			return coveredBy(r, needed)
		}
	}
	g := c.newSeriesGrabber(s, profile, byName, pending, sc.Replace, scope)
	left := planSeriesGrabs(planInput{
		eligible:      decision.Eligible,
		wanted:        wanted,
		seriesSeasons: seriesSeasons,
		counts:        seasonEpisodeCounts(s),
		ended:         showEnded(s.Status),
		opts: planOpts{
			Scoped:            true,
			EpisodesOnly:      episodeOnly,
			AllowPackFallback: !episodeOnly && !sc.Replace,
		},
		cover: cover,
		try:   func(name, label string) bool { return g.try(ctx, name, label) },
		log:   c.log,
		title: s.Title,
	})
	out.Grabbed, out.Pending = g.titles, g.pendingHits

	switch {
	case len(out.Grabbed) == 0 && out.Pending == 0 && episodeOnly && onlyPacksFit(decision.Eligible):
		out.Note = "Only season packs are available — use Choose release"
	case len(left) > 0 && len(left) < len(wanted):
		out.Note = fmt.Sprintf("%d episode%s still missing", len(left), plural(len(left)))
	}
	return out, nil
}

// onlyPacksFit reports whether every release that fit the profile was a pack — the reason
// an episode click grabbed nothing despite eligible results.
func onlyPacksFit(eligible []quality.Evaluation) bool {
	for _, ev := range eligible {
		if ev.Candidate.Release.Kind() == parser.KindEpisode {
			return false
		}
	}
	return len(eligible) > 0
}

// reportSearched writes a click's outcome to the show's history and the bus. The
// automatic sweep doesn't come through here: it runs every few minutes, and its outcomes
// are in the log.
func (c *Coordinator) reportSearched(ctx context.Context, s series.Series, sc SeriesScope, out GrabOutcome) {
	summary := out.Summary()
	c.series.AddEvent(ctx, s.ID, "searched", sc.label()+": "+summary)
	c.log.Info("series: scoped search", "series", s.Title, "scope", sc.label(), "trigger", sc.Trigger, "outcome", summary)
	if c.bus != nil {
		c.bus.Publish("series.searched", map[string]any{"id": s.ID, "scope": out.Scope, "outcome": out})
	}
}
