package automation

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/pathguard"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// RankSeriesReleases runs an interactive search for a series, optionally scoped to a
// season and episode (>0), scoring every relevant release against the series' quality
// profile and returning them ranked best-first — WITHOUT grabbing. This is the manual
// "search indexers" backend, shared by the season- and episode-level UI.
//
// A negative season is the whole show; season 0 is Specials, which lists only releases
// tagged S00.
func (c *Coordinator) RankSeriesReleases(ctx context.Context, seriesID int64, season, episode int) (ReleaseList, error) {
	return c.RankSeriesReleasesWith(ctx, seriesID, season, episode, nil)
}

// RankSeriesReleasesWith ranks under spec instead of the show's own profile when it's set
// (the quality builder's "test on a real title").
func (c *Coordinator) RankSeriesReleasesWith(ctx context.Context, seriesID int64, season, episode int, spec *quality.StoredProfile) (ReleaseList, error) {
	if c.series == nil {
		return ReleaseList{}, nil
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return ReleaseList{}, err
	}
	// One collector across the broad, per-season, specials and alias queries, so the
	// banner lists each failing indexer once whichever query it failed.
	ctx, notes := withSearchNotes(ctx)
	ctx = c.withSeriesFit(ctx)
	if ctx, err = c.withSameNameJudge(ctx, s); err != nil { // #81, before any indexer is asked
		return ReleaseList{}, err
	}
	releases, _, err := c.searchSeriesScope(ctx, s, season, episode)
	if allIndexersFailed(err) {
		return ReleaseList{Profile: c.effectiveProfile(ctx, s.QualityProfile, quality.MediaSeries)}.withIssues(notes), nil
	}
	if err != nil {
		return ReleaseList{}, err
	}

	// Another library show with this one's name (#81): releases that could as well be its
	// are listed apart, saying so, and still grabbable by hand.
	same, err := c.sameNameFor(ctx, s)
	if err != nil {
		return ReleaseList{}, err
	}
	sameWhy := map[string]string{}
	byName := make(map[string]indexer.Release, len(releases))
	cands := make([]quality.Candidate, 0, len(releases))
	var unusable []quality.Candidate // listed, but ranked apart: no client can take them
	var sameNamed []quality.Candidate
	var droppedTitle, droppedScope int
	var sampleDropped, sampleScope []string
	rts := newRuntimeIndex(s)
	nets := c.networks(ctx)
	usable, others := nets.split(releases)
	byTitle := map[string]int{} // specials found by their title rather than S00Exx
	fit := c.seriesFit(ctx)
	for _, rel := range append(usable, others...) {
		p := parser.Parse(rel.Title)
		if season == 0 {
			// A special named by its title: its name is the show plus that title, so the
			// identity check below would read it as another show.
			n := specialByTitle(p, rel.Title, s, 0)
			if n > 0 && episode > 0 && n != episode {
				droppedScope++ // another special
				continue
			}
			if n > 0 {
				byName[rel.Title] = rel
				byTitle[rel.Title] = n
				cand := c.specialCandidate(c.newSeriesCandidate(ctx, s, rts, rel), rts, n)
				if !nets.canGrab(rel) {
					unusable = append(unusable, cand)
				} else {
					cands = append(cands, cand)
				}
				continue
			}
		}
		if ok, why := searchIdentity(fit, p, s); !ok {
			droppedTitle++
			if len(sampleDropped) < 8 {
				label := rel.Title + " → " + p.Title
				if why != "" {
					label += " (" + why + ")" // the right title, but another show's year or country
				}
				sampleDropped = append(sampleDropped, label)
			}
			continue // a different show that merely shares a title prefix (e.g. "Below Deck Mediterranean" for "Below Deck")
		}
		if !c.releaseMatchesScope(ctx, s, p, season, episode) {
			droppedScope++
			// What a right-show release DID resolve to is the answer to "there are
			// torrents for this episode, why won't it take them?" — usually that they
			// are other episodes entirely, which no amount of title matching fixes.
			if len(sampleScope) < 8 {
				sampleScope = append(sampleScope, rel.Title+" → "+c.resolvedLabel(ctx, s, p))
			}
			continue // not relevant to the requested season/episode scope
		}
		byName[rel.Title] = rel
		if !nets.canGrab(rel) {
			unusable = append(unusable, c.newSeriesCandidate(ctx, s, rts, rel))
			continue
		}
		if refuse, why := same.refuses(rel); refuse {
			sameWhy[rel.Title] = "Not grabbed automatically: " + why
			sameNamed = append(sameNamed, c.newSeriesCandidate(ctx, s, rts, rel))
			continue
		}
		cands = append(cands, c.newSeriesCandidate(ctx, s, rts, rel))
	}
	c.log.Info("series: search filtered", "series", s.Title, "kept", len(cands), "dropped_wrong_title", droppedTitle, "dropped_out_of_scope", droppedScope)
	if len(sampleDropped) > 0 {
		c.log.Info("series: sample of dropped titles (parsed → title)", "series", s.Title, "samples", strings.Join(sampleDropped, " | "))
	}
	if len(sampleScope) > 0 {
		c.log.Info("series: sample of right-show releases that were other episodes (release → resolved)",
			"series", s.Title, "want_season", season, "want_episode", episode,
			"samples", strings.Join(sampleScope, " | "))
	}
	profile := c.effectiveProfile(ctx, s.QualityProfile, quality.MediaSeries)
	decision := c.decideWith(ctx, profile, spec, cands)
	apart := c.decideWith(ctx, profile, spec, unusable)
	var apartSame quality.Decision
	if len(sameNamed) > 0 {
		apartSame = c.decideWith(ctx, profile, spec, sameNamed)
	}

	winnerName := ""
	if decision.Winner != nil {
		winnerName = decision.Winner.Candidate.Name
	}
	// Flag blocklisted releases like the movie path does, so the UI can warn before the
	// user re-grabs a release that just stalled or imported as junk.
	blocked, err := c.blockedSetSeries(ctx, s.ID)
	if err != nil {
		return ReleaseList{}, err
	}
	out := make([]RankedRelease, 0, len(cands))
	// Each candidate carries the runtime it covers — summed across a pack's episodes — so
	// packs and multi-episode files show a real bitrate too, not only single episodes.
	// ranked: the release is in the main ranking (what a client can take), so its place
	// there can be explained.
	appendEval := func(ev quality.Evaluation, ranked bool) {
		rel := byName[ev.Candidate.Name]
		resolves := c.resolvesLabel(ctx, s, ev.Candidate.Release)
		if n := byTitle[ev.Candidate.Name]; n > 0 {
			resolves = specialLabel(n)
		}
		out = append(out, explainRow(withIndexerFacts(RankedRelease{
			Title:        ev.Candidate.Name,
			Indexer:      rel.Indexer,
			DownloadURL:  rel.DownloadURL,
			InfoHash:     rel.InfoHash,
			InfoURL:      safeInfoURL(rel.InfoURL, rel.DownloadURL),
			SizeGB:       ev.Candidate.SizeGB,
			Bitrate:      bitrateMbps(ev.Candidate.SizeGB, ev.Candidate.RuntimeMin),
			Seeders:      ev.Candidate.Seeders,
			Summary:      summarizeSeries(ev.Candidate.Release),
			Eligible:     ev.Eligible,
			RejectReason: ev.RejectReason,
			Recommended:  ev.Candidate.Name == winnerName,
			Blocklisted:  blocked[normTitle(ev.Candidate.Name)],
			Resolves:     resolves,
		}, rel, nets), ev, ranked))
	}
	for _, ev := range decision.Eligible {
		appendEval(ev, true)
	}
	for _, evs := range [][]quality.Evaluation{decision.Rejected, apart.Eligible, apart.Rejected} {
		for _, ev := range evs {
			appendEval(ev, false)
		}
	}
	for _, evs := range [][]quality.Evaluation{apartSame.Eligible, apartSame.Rejected} {
		for _, ev := range evs {
			if ev.Eligible {
				// The profile would take it; the name is what holds it back.
				ev.Eligible, ev.RejectReason = false, sameWhy[ev.Candidate.Name]
			}
			appendEval(ev, false)
		}
	}
	return ReleaseList{Profile: profile, Why: decision.Why, ChosenOver: decision.ChosenOver, Releases: out}.withIssues(notes), nil
}

// searchSeriesScope runs the indexer queries for a season/episode scope (a negative season
// is the whole show, season 0 is Specials) and returns everything they found, unfiltered,
// plus the per-indexer errors of the main query. Shared by the interactive list and the quick Grab buttons, so
// a button searches exactly where the list the user would see does.
func (c *Coordinator) searchSeriesScope(ctx context.Context, s series.Series, season, episode int) ([]indexer.Release, map[string]string, error) {
	abs := 0
	if s.IsAnime() && season > 0 && episode > 0 {
		abs = c.series.AbsoluteNumber(ctx, s.ID, season, episode)
	}
	queries := seriesScopeQueries(s, season, episode, abs, c.series.AliasSearchTerms(ctx, s.ID, season, episode))
	q := queries[0]
	title := q.Text

	result, err := c.search(ctx, q)
	if err != nil {
		return nil, result.Errors, err
	}
	// Browsing the whole show: fan out per season as well, for the same reason the
	// automatic search does. One title query can't surface nine seasons' worth of packs.
	if season < 0 && !s.IsAnime() {
		result.Releases = c.addSeasonSearches(ctx, s, title, result.Releases)
	}
	// Specials: name each one, since the broad title query is one capped page of mostly
	// regular episodes and there's no parameter that asks for season 0. Most specials are
	// released by name rather than S00Exx, so each is also searched by its title.
	if season == 0 {
		for _, sq := range specialQueries(s, title, episode) {
			sres, serr := c.search(ctx, indexer.SearchQuery{Text: sq, MediaType: indexer.MediaSeries, Limit: 100})
			if serr != nil {
				c.log.Warn("series: specials search failed", "series", s.Title, "query", sq, "err", serr)
				continue
			}
			result.Releases = append(result.Releases, sres.Releases...)
		}
	}
	// The targeted and alias queries (seriesScopeQueries says why each exists), and one by
	// the show's translated title in the owner's extra title languages when it has one
	// worth asking by. One failing doesn't sink the search: the main query above already
	// answered.
	if tq, ok := translatedScopeQuery(c.seriesFit(ctx), s, season, episode, queries); ok {
		queries = append(queries, tq)
	}
	for _, extra := range queries[1:] {
		res, xerr := c.search(ctx, extra)
		if xerr != nil {
			c.log.Warn("series: extra search failed", "series", s.Title, "query", extra.Text, "err", xerr)
			continue
		}
		c.log.Info("series: extra search", "series", s.Title, "query", extra.Text, "returned", len(res.Releases))
		result.Releases = append(result.Releases, res.Releases...)
	}
	// Log the REQUESTED scope, not the query parameters. For anime the query is
	// deliberately left broad (q.Season/q.Episode stay 0 so every numbering convention
	// is returned), so printing those said "season=0 episode=0" for a search of one
	// specific episode — which reads as "it searched everything and found nothing".
	c.log.Info("series: interactive search", "series", s.Title, "query", title,
		"want_season", season, "want_episode", episode,
		"query_season", q.Season, "query_episode", q.Episode,
		"raw", len(result.Releases), "indexer_errors", len(result.Errors))
	for name, e := range result.Errors {
		c.log.Warn("series: indexer error", "indexer", name, "err", e)
	}
	return result.Releases, result.Errors, nil
}

// maxSpecialQueries bounds the per-special searches of one Specials browse, so a show
// with dozens of missing specials can't flood an indexer.
const maxSpecialQueries = 5

// seriesScopeQueries are the indexer queries for one season/episode scope, main query
// first. Pure, so what an interactive search asks for can be tested without an indexer.
//
//   - The main query: the cleaned title (releases carry no punctuation, so "Teen Titans
//     Go!" is searched as "Teen Titans Go"), with season and episode in the tvsearch
//     PARAMETERS for a standard show — indexers match those far better than "Title S07" as
//     text. Anime stays broad: it's released under absolute "- 137", per-cour SxxExx and
//     split-season S02 alike, and the resolver-backed scope filter picks what covers the
//     episode. Specials can't use the parameters either: to tvsearch, season 0 is none.
//   - The episode by the arc's own numbering (aliasTerms, "BLEACH Thousand-Year Blood War
//     15"), and for anime by its absolute number (absoluteQueries, "Frieren 13"). Broad
//     queries are capped — TorrentLeech answers a bare q= with 35 rows, newest first — so
//     an episode from two years ago is only reachable by naming it.
//   - Each alias by name (searchAliases): the indexer doesn't know an arc's name or a
//     romaji title is the same show, so the series' title never returns those releases.
//
// Duplicates are dropped.
func seriesScopeQueries(s series.Series, season, episode, abs int, aliasTerms []string) []indexer.SearchQuery {
	title := indexerQuery(s.Title)
	main := seriesQuery(s, title, 400)
	if !s.IsAnime() && season > 0 {
		main.Season, main.Episode = season, episode
	}
	out := []indexer.SearchQuery{main}
	seen := map[string]bool{title: true}
	add := func(text string) {
		if text == "" || seen[text] {
			return
		}
		seen[text] = true
		out = append(out, indexer.SearchQuery{Text: text, MediaType: indexer.MediaSeries, Limit: 400})
	}
	for _, t := range aliasTerms {
		add(indexerQuery(t))
	}
	if s.IsAnime() && season > 0 && episode > 0 {
		for _, q := range absoluteQueries(s, abs) {
			add(q)
		}
	}
	for _, a := range searchAliases(s) {
		add(indexerQuery(a.Title))
	}
	return out
}

// specialsToQuery lists the specials a season-0 search names: the one asked for, or the
// aired specials still missing a file, at most maxSpecialQueries.
func specialsToQuery(s series.Series, episode int) []int {
	if episode > 0 {
		return []int{episode}
	}
	var out []int
	for _, sn := range s.Seasons {
		if sn.SeasonNumber != 0 {
			continue
		}
		for _, e := range sn.Episodes {
			if len(out) < maxSpecialQueries && !e.HasFile && aired(e.AirDate) {
				out = append(out, e.EpisodeNumber)
			}
		}
	}
	return out
}

// specialQueries are a Specials search's per-special queries: "Title S00E05" for each
// special specialsToQuery names, and "Title Its Special Title" for those whose title is
// worth searching. Duplicates are dropped.
func specialQueries(s series.Series, title string, episode int) []string {
	var out []string
	seen := map[string]bool{}
	add := func(q string) {
		if q != "" && !seen[q] {
			seen[q] = true
			out = append(out, q)
		}
	}
	for _, n := range specialsToQuery(s, episode) {
		add(fmt.Sprintf("%s S00E%02d", title, n))
		if ep, ok := specialEpisode(s, n); ok {
			add(specialTitleQuery(title, ep))
		}
	}
	return out
}

// isSpecialRelease reports whether a release is a Specials episode: an episode release
// that spelled out season 0 ("S00E05", "0x05"), carrying the wanted episode (any, when
// episode is 0). Nothing else may stand in for a special — a pack "covers" season 0 only
// because it covers the whole run, an absolute-numbered "[Grp] Show - 05" is a regular
// episode, and an alias' numbering has no specials.
func isSpecialRelease(p parser.Release, episode int) bool {
	if p.Kind() != parser.KindEpisode || !p.SeasonExplicit || p.Season != 0 {
		return false
	}
	if episode <= 0 {
		return true
	}
	for _, e := range p.Episodes {
		if e == episode {
			return true
		}
	}
	return false
}

// releaseMatchesScope is seriesReleaseMatches with anime awareness: an episode-scope
// anime release is resolved through absolute/positional numbering before checking it
// covers the requested (season, episode).
func (c *Coordinator) releaseMatchesScope(ctx context.Context, s series.Series, p parser.Release, season, episode int) bool {
	// Specials first: no numbering rule — alias, absolute or scene — maps onto season 0.
	if season == 0 {
		return isSpecialRelease(p, episode)
	}
	// An alias-titled release carries the alias' numbering, so resolve it that way
	// before any of the series' own numbering rules get a look.
	if refs, ok := c.series.AliasEpisodes(ctx, s.ID, p); ok {
		if season < 0 {
			return true
		}
		for _, ref := range refs {
			if ref.Season == season && (episode <= 0 || ref.Episode == episode) {
				return true
			}
		}
		return false
	}
	if !s.IsAnime() {
		return seriesReleaseMatches(p, season, episode)
	}
	if season < 0 {
		return p.IsTV()
	}
	if p.Kind() != parser.KindEpisode {
		if p.CoversSeason(season) {
			return true // real-season pack matching the requested TMDB season
		}
		// A split-season pack ("Frieren S02") for a season TMDB doesn't have.
		if p.Season > 0 && !p.Complete && len(p.Seasons) <= 1 && !c.series.HasSeason(ctx, s.ID, p.Season) {
			for _, ref := range c.series.SceneSeasonEpisodes(ctx, s.ID, p.Season) {
				if ref.Season == season && (episode <= 0 || ref.Episode == episode) {
					return true
				}
			}
		}
		return false
	}
	for _, ref := range c.series.ResolveEpisodes(ctx, s.ID, p) {
		if ref.Season == season && (episode <= 0 || ref.Episode == episode) {
			return true
		}
	}
	return false
}

// seriesReleaseMatches reports whether a release is relevant to a season/episode scope.
// season < 0 (the whole show) → any TV release; season 0 → an S00-tagged special; a season
// with episode <= 0 → covers that season; both set → the exact episode, or a pack that
// covers it.
func seriesReleaseMatches(p parser.Release, season, episode int) bool {
	if season < 0 {
		return p.IsTV()
	}
	if season == 0 {
		return isSpecialRelease(p, episode)
	}
	if !p.CoversSeason(season) {
		return false
	}
	if episode <= 0 {
		return true
	}
	if p.Kind() == parser.KindEpisode {
		for _, e := range p.Episodes {
			if e == episode {
				return true
			}
		}
		return false
	}
	return true // a season/multi-season/complete pack covers the episode
}

// summarizeSeries renders a release's tier + quality in plain language, e.g.
// "Season 2 pack · 1080p · WEB-DL" or "S02E05 · 4K".
func summarizeSeries(r parser.Release) string {
	tier := ""
	switch r.Kind() {
	case parser.KindCompleteShow:
		tier = "Complete series"
	case parser.KindMultiSeason:
		tier = "Seasons " + joinSeasons(r.Seasons)
	case parser.KindSeasonPack:
		tier = fmt.Sprintf("Season %d pack", r.Season)
	case parser.KindEpisode:
		if r.Season > 0 && len(r.Episodes) > 0 {
			tier = fmt.Sprintf("S%02dE%02d", r.Season, r.Episodes[0])
		}
	}
	q := summarize(r) // reuse the movie quality summary (resolution/HDR/source)
	switch {
	case tier == "":
		return q
	case q == "Standard quality":
		return tier
	default:
		return tier + " · " + q
	}
}

func joinSeasons(seasons []int) string {
	parts := make([]string, 0, len(seasons))
	for _, s := range seasons {
		parts = append(parts, strconv.Itoa(s))
	}
	return strings.Join(parts, ", ")
}

// GrabForSeries resolves a release and hands it to the download client in the series
// category, recorded as a series grab (so seed cleanup manages it like an auto grab).
// scope is the modal it was picked from — the whole show, a season or an episode — and
// limits which episodes skip the import gate.
func (c *Coordinator) GrabForSeries(ctx context.Context, seriesID int64, indexerName, downloadURL, title string, scope GrabScope) error {
	// A pick from the Specials list that names one special by title is for that special:
	// its file carries no numbering, and the import places it by this scope.
	scope = c.narrowSpecialScope(ctx, seriesID, title, scope)
	return c.grabForSeries(ctx, seriesID, indexerName, downloadURL, title, true, scope)
}

// GrabForSeriesAuto is the same grab made by the automation rather than the user, so the
// import gate still applies: a sweep must not replace a good file with a worse one.
func (c *Coordinator) GrabForSeriesAuto(ctx context.Context, seriesID int64, indexerName, downloadURL, title string) error {
	return c.grabForSeries(ctx, seriesID, indexerName, downloadURL, title, false, WholeShow)
}

func (c *Coordinator) grabForSeries(ctx context.Context, seriesID int64, indexerName, downloadURL, title string, manual bool, scope GrabScope) error {
	s, serr := c.series.Get(ctx, seriesID)
	if serr == nil {
		ctx = c.forSeries(ctx, s) // saved beside the show's folder when it has its own downloads folder
	}
	hash, err := c.grabTo(ctx, indexerName, downloadURL, title, seriesCategory)
	if err != nil {
		return err
	}
	if serr == nil {
		c.recordSeriesGrab(ctx, seriesID, title, indexerName, s.QualityProfile, hash)
	}
	// manual: picked out of the interactive search (or Replace), so the import gate must
	// not second-guess it on score — inside scope. Anything else the release carries is
	// gated like an automatic grab. An automatic whole-show grab is the column defaults,
	// so there's nothing to write.
	if manual || scope != WholeShow {
		c.markGrab(ctx, hash, manual, scope)
	}
	if manual && serr == nil {
		c.reopenImportedRelease(ctx, seriesID, hash, title)
	}
	c.series.AddEvent(ctx, seriesID, "grabbed", title+" · "+indexerName)
	return nil
}

// reopenImportedRelease lets a release the user grabs again by hand be imported again.
//
// The import sweep passes over a torrent already recorded as imported, and over one
// blocklisted for the show. A re-grab of the same torrent keeps its info hash, so it was
// handed to the client and then never looked at: Nobody Wants This S03, misplaced on its
// first import, was re-processed a sweep later, placed nothing new, and was blocklisted
// as "fully imported" — so grabbing it again by hand silently did nothing. The user picking
// it again is the say-so: its import record and this show's blocklist entry for it go, and
// the next sweep imports it like a fresh manual grab, quality gate skipped inside its scope.
// Global blocklist entries (fakes, executables) stay.
//
// Only a show's import record is reopened: one written by the series import (no film, no
// paths) for a torrent no other kind of grab knows. A film imported from the same hash is
// never touched — and a torrent another kind of grab shares isn't reopened at all; it
// can still be imported by hand.
//
// The torrent's earlier grabs are retired (superseded) first. Seed cleanup reads the grab
// a torrent came from, and an old 'imported' row still said its data may go with it —
// after this re-grab was held and dismissed, that deleted the only copies of the files the
// user had taken over.
func (c *Coordinator) reopenImportedRelease(ctx context.Context, seriesID int64, hash, title string) {
	if hash == "" || c.db == nil {
		return
	}
	const showImport = `lower(download_hash) = lower(?) AND movie_id = 0 AND source_path = ''
		AND NOT EXISTS (SELECT 1 FROM grabs WHERE info_hash != '' AND lower(info_hash) = lower(?) AND media_type != 'series')`
	var name string
	err := c.db.QueryRowContext(ctx, `SELECT title FROM imports WHERE `+showImport+` LIMIT 1`, hash, hash).Scan(&name)
	if err != nil {
		return // never imported as a show's (or unreadable): a fresh grab, nothing to reopen
	}
	old, err := c.grabsWhere(ctx, seedCleanupWhere+` AND media_type = 'series' AND info_hash != '' AND lower(info_hash) = lower(?)`, hash)
	if err != nil {
		c.log.Warn("grab: couldn't read the earlier grabs of a release grabbed again by hand — leaving it as it was", "release", title, "err", err)
		return
	}
	ids := make([]int64, 0, len(old))
	unmatched := 0
	for _, g := range old {
		ids = append(ids, g.ID)
		unmatched = max(unmatched, g.ImportUnmatched)
	}
	c.setGrabStatuses(ctx, ids, grabStatusSuperseded)
	// Files an earlier import couldn't match are still only in the download. The mark that
	// keeps it seeding moves to the new grab (it only ever rises), or retiring the old rows
	// would let the new one's seed goal delete them.
	c.noteImportUnmatched(ctx, hash, title, unmatched)
	if _, err := c.db.ExecContext(ctx,
		`DELETE FROM blocklist WHERE movie_id = ? AND media_type = 'series' AND norm_title IN (?, ?)`,
		seriesID, normTitle(title), normTitle(name)); err != nil {
		c.log.Warn("grab: couldn't lift the show's blocklist entry for a release grabbed again by hand", "release", title, "err", err)
		return
	}
	if _, err := c.db.ExecContext(ctx, `DELETE FROM imports WHERE `+showImport, hash, hash); err != nil {
		c.log.Warn("grab: couldn't reopen an imported release grabbed again by hand", "release", title, "err", err)
		return
	}
	c.log.Info("grab: release grabbed again by hand — it will be imported again on the user's say-so",
		"release", title, "download", name, "earlier_grabs_retired", len(ids))
}

// RescanSeries reconciles a series' episode records with what's actually on disk —
// the "rescan" half of Refresh & rescan. It walks the show's real library folder
// (not the derived name, so a differently-named folder still works), marks every
// episode file it finds present (updating moved paths), and clears episodes whose
// file is gone — so deleting a stray/duplicate folder is picked up cleanly.
//
// Clearing an episode makes it wanted again, so a TV share that isn't mounted would
// otherwise have every episode searched and downloaded a second time. Like the movies
// missing-file check, it clears nothing unless the folders those files lived in are
// clearly there; when they aren't it changes nothing at all and returns a
// *RescanRefusedError saying why.
func (c *Coordinator) RescanSeries(ctx context.Context, seriesID int64) error {
	_, err := c.rescanSeries(ctx, seriesID, false)
	return err
}

// ForgetMissingEpisodes is RescanSeries for someone who says the files are gone on purpose
// (#97): the show's folder being missing, or every file having gone at once, no longer stops
// the episodes clearing. It deletes nothing, and clears only records whose file is definitely
// gone. An unmounted TV folder still refuses: nobody meant that. It returns how many it cleared.
func (c *Coordinator) ForgetMissingEpisodes(ctx context.Context, seriesID int64) (int, error) {
	n, err := c.rescanSeries(ctx, seriesID, true)
	if err == nil && n > 0 && c.series != nil {
		c.series.AddEvent(ctx, seriesID, "removed", fmt.Sprintf("Forgot %d missing file(s), asked for by hand. Nothing on disk was touched", n))
	}
	return n, err
}

func (c *Coordinator) rescanSeries(ctx context.Context, seriesID int64, confirmedGone bool) (int, error) {
	if c.series == nil || c.imp == nil {
		return 0, nil
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return 0, err
	}
	// Before anything reads as missing: a file an interrupted rename left under its
	// temporary name would otherwise be cleared below and downloaded again.
	c.restoreRenameTemps(s)
	// Never walk a folder other shows' files are in: every file there that parses to one
	// of this show's episode numbers would be claimed as its own.
	imp, folder, ferr := c.seriesPlacement(ctx, s) // folder "" → importer derives the name
	// The way out for a show whose folder can't be told: files moved by hand into a folder
	// of its own are followed there, then the show is judged again.
	if ferr != nil && c.series.FollowMovedEpisodeFiles(ctx, seriesID) > 0 {
		if s, err = c.series.Get(ctx, seriesID); err != nil {
			return 0, err
		}
		imp, folder, ferr = c.seriesPlacement(ctx, s)
	}
	if ferr != nil {
		return 0, &RescanRefusedError{Reason: ferr.Error(), Cause: ferr}
	}

	// Group by resolved episode first, rather than marking as we walk. Several files on
	// disk can claim the same episode — three copies of every Solo Leveling season 2
	// episode, say, left by re-imports under different names. Marking inside the walk made
	// the LAST file the directory happened to yield the winner, silently overwrote the
	// others' paths, and reported nothing: the extra copies became invisible to Arrmada
	// while still filling the disk and showing up as duplicates in Plex.
	byEpisode := map[[2]int][]library.FoundVideo{}
	for _, v := range c.showEpisodeVideos(ctx, s, imp, folder) {
		// Run each file through the full resolver: SxxExx, multi-episode, and — for anime
		// — absolute ("S2 29") and per-cour numbering all map onto the metadata's episodes.
		for _, ref := range c.series.ResolveEpisodes(ctx, seriesID, library.ParseEpisodeFile(v.Path)) {
			key := [2]int{ref.Season, ref.Episode}
			byEpisode[key] = append(byEpisode[key], v)
		}
	}

	// Episodes still flagged as having a file that wasn't found on disk and whose stored
	// path no longer exists (e.g. a deleted duplicate season folder). Only "no such file"
	// counts: a file that can't be read right now is not gone.
	var gone []series.Episode
	recorded := 0
	for _, sn := range s.Seasons {
		for _, e := range sn.Episodes {
			if !e.HasFile {
				continue
			}
			recorded++
			if len(byEpisode[[2]int{e.SeasonNumber, e.EpisodeNumber}]) > 0 {
				continue
			}
			if e.FilePath == "" || fileGone(e.FilePath) {
				gone = append(gone, e)
			}
		}
	}
	if len(gone) > 0 {
		var reason string
		if confirmedGone {
			reason = forgetRefusal(c.series.TVRootOf, gone)
		} else {
			reason = rescanRefusal(imp.TVRoot(), s.Title, gone, recorded, c.series.ShowFolderFor)
		}
		if reason != "" {
			c.log.Warn("series rescan: the files' folders don't look mounted — nothing was changed",
				"series", s.Title, "would_clear", len(gone), "reason", reason)
			return 0, &RescanRefusedError{Reason: reason}
		}
	}
	if err := ctx.Err(); err != nil {
		return 0, err // a rescan cut short writes nothing
	}

	// Which file an episode keeps (keptEpisodeFile): its recorded one, unless a file named as
	// the show — read by its name, not only by the on-disk "1x8" rule (#81) — competes. A
	// file read on the benefit of the doubt (another show's name in the show's own Season 1,
	// a name with no title) or only as "1x8" never displaces the file an episode has; it may
	// only fill an episode that has none.
	recordedAt := recordedEpisodeFiles(s)
	dupes := 0
	for key, found := range byEpisode {
		best, files, ok := keptEpisodeFile(found, recordedAt[key], s)
		if !ok {
			dupes += len(found)
			c.log.Info("series rescan: an episode keeps its recorded file; another copy whose name isn't the show's, or read only as a library file, is left as it is",
				"series", s.Title, "episode", fmt.Sprintf("S%02dE%02d", key[0], key[1]),
				"kept", filepath.Base(recordedAt[key]), "file", filepath.Base(found[0].Path))
			continue
		}
		if len(files) > 1 {
			dupes += len(files) - 1
			others := make([]string, 0, len(files)-1)
			for _, f := range files {
				if f.Path != best.Path {
					others = append(others, filepath.Base(f.Path))
				}
			}
			c.log.Warn("series rescan: several files claim the same episode — keeping the best one; the rest are duplicates taking up space",
				"series", s.Title, "episode", fmt.Sprintf("S%02dE%02d", key[0], key[1]),
				"keeping", filepath.Base(best.Path), "duplicates", strings.Join(others, ", "))
		}
		// A different file than the episode recorded doesn't inherit the old one's release.
		_ = c.series.AdoptFoundEpisodeFile(ctx, seriesID, key[0], key[1], best.Path, best.Size)
	}
	if dupes > 0 {
		c.log.Warn("series rescan: duplicate episode files on disk", "series", s.Title, "extra_files", dupes)
	}
	cleared := 0
	for _, e := range gone {
		err := c.series.MarkEpisodeMissing(ctx, seriesID, e.SeasonNumber, e.EpisodeNumber)
		if err == nil {
			cleared++
		} else if confirmedGone {
			return cleared, err // someone is waiting to be told how many
		}
	}
	return cleared, nil
}

// showEpisodeVideos is the show's episode videos for a rescan or the duplicate check, which
// read exactly the same files: those in its folder, minus Plex extras folders' (library.
// SeriesEpisodeVideos), that series.EpisodeFileFilter lets through — so neither ever claims
// or offers for deletion a file in another show's folder inside it, or another show's file.
func (c *Coordinator) showEpisodeVideos(ctx context.Context, s series.Series, imp *library.Importer, folder string) []library.FoundVideo {
	if folder == "" {
		// No files recorded yet: only the exact "<Title> (<Year>)" folder right in the TV
		// folder is read, and only when it's provably the show's own (series.
		// NoFilesFolderUnproven). Never a year-less "<Title>": it may be a namesake's (the UK
		// "The Office" beside the US one). Otherwise nothing is read; a hand import gives the
		// show its files.
		name := library.YearFolderName(s.Title, s.Year)
		if name == "" {
			return nil
		}
		if why := c.series.NoFilesFolderUnproven(ctx, s, filepath.Join(imp.TVRoot(), name)); why != "" {
			c.log.Info("series rescan: the show's \"Title (Year)\" folder isn't provably its own — not read", "series", s.Title, "folder", name, "why", why)
			return nil
		}
		folder = name
	}
	dir, vids := imp.SeriesEpisodeVideos(folder, s.Title, s.Year)
	if dir == "" {
		return nil
	}
	keep := c.series.EpisodeFileFilter(ctx, s, dir)
	// A show kept in season folders doesn't keep episodes loose in its folder: a video there
	// it hasn't recorded ("Show - S01E01 - Pilot (Unaired).mkv") is something else, never
	// claimed nor offered for deletion. A flat show (or one with no files yet) reads them.
	recorded, loose := 0, false
	for _, sn := range s.Seasons {
		for _, e := range sn.Episodes {
			if e.HasFile && e.FilePath != "" {
				recorded++
				loose = loose || filepath.Dir(filepath.Clean(e.FilePath)) == filepath.Clean(dir)
			}
		}
	}
	seasonsOnly := recorded > 0 && !loose
	out := vids[:0:0]
	for _, v := range vids {
		if !keep(v.Path) {
			continue
		}
		if seasonsOnly && filepath.Dir(filepath.Clean(v.Path)) == filepath.Clean(dir) {
			continue
		}
		out = append(out, v)
	}
	return out
}

// RescanRefusedError is a series rescan that changed nothing because the folders its
// files live in didn't look mounted. Reason is written for the person who started it.
type RescanRefusedError struct {
	Reason string
	// Cause is why, when it was something other than a folder looking unmounted (a
	// series.ShowFolderError: the show's folder can't be told).
	Cause error
}

// Unwrap is the refusal's cause, if any.
func (e *RescanRefusedError) Unwrap() error { return e.Cause }

func (e *RescanRefusedError) Error() string { return e.Reason }

// rescanRefusal says why a rescan must not clear the gone episodes ("" when it may). It
// follows the movies missing-file check: a file only counts as missing when the folder
// it would be in is clearly there (exists and isn't empty). For a show that is the TV
// folder for files inside it, and the show's own folder for every file — one disk of a
// split share can be down while the TV folder itself looks fine. Every recorded file of
// a show vanishing at once is refused too: that's what a missing disk looks like far
// more often than a deliberate deletion, and clearing them all would download the whole
// show again.
func rescanRefusal(root, title string, gone []series.Episode, recorded int, showFolder func(string) string) string {
	if reason := rootRefusal(root, gone); reason != "" {
		return reason
	}
	const onPurpose = " If you deleted them on purpose, choose Forget missing files in the show's ⋯ menu."
	var shows []string
	seen := map[string]bool{}
	for _, e := range gone {
		if e.FilePath == "" {
			continue // a row with no path has no folder to judge
		}
		if dir := showFolder(filepath.Clean(e.FilePath)); dir != "" && !seen[dir] {
			seen[dir] = true
			shows = append(shows, dir)
		}
	}
	sort.Strings(shows)
	for _, dir := range shows {
		if !library.FolderLooksMounted(dir) {
			return fmt.Sprintf("The folder for %s isn't reachable (%s is missing or empty) — nothing was changed. A disk or share may be down; check it, then try again."+onPurpose, title, dir)
		}
	}
	if recorded > 1 && len(gone) == recorded {
		return fmt.Sprintf("None of the %d files for %s are where they were — nothing was changed. A whole show vanishing at once usually means a disk or share is down; check it, then try again."+onPurpose, recorded, title)
	}
	return ""
}

// forgetRefusal is what someone saying the files are gone on purpose can't wave through: a gone
// file whose own TV folder looks unmounted, or that's in no TV folder Arrmada knows, so nothing
// can tell a deletion from a dead disk. A show's files can be split across TV folders, so each
// file is judged by its own.
func forgetRefusal(rootOf func(string) (string, bool), gone []series.Episode) string {
	for _, e := range gone {
		if e.FilePath == "" {
			continue
		}
		p := filepath.Clean(e.FilePath)
		root, ok := rootOf(p)
		if !ok {
			return fmt.Sprintf("%s isn't in any of your TV folders, so Arrmada can't tell whether its disk is down — nothing was changed.", p)
		}
		if !library.FolderLooksMounted(root) {
			return fmt.Sprintf("The TV folder isn't reachable (%s is missing or empty) — nothing was changed. Check the share or array is mounted, then try again.", root)
		}
	}
	return ""
}

// rootRefusal is gone files inside a TV folder that itself looks unmounted.
func rootRefusal(root string, gone []series.Episode) string {
	root = strings.TrimSpace(root)
	if root == "" || library.FolderLooksMounted(root) {
		return ""
	}
	for _, e := range gone {
		if e.FilePath != "" && pathguard.Under(filepath.Clean(e.FilePath), root) {
			return fmt.Sprintf("The TV folder isn't reachable (%s is missing or empty) — nothing was changed. Check the share or array is mounted, then try again.", root)
		}
	}
	return ""
}

// fileGone reports whether path definitely doesn't exist. An error reading it (a share
// that errors, permissions) is not the same as gone.
func fileGone(path string) bool {
	_, err := os.Stat(path)
	return errors.Is(err, fs.ErrNotExist)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// SeriesImportCandidate is a video file on disk that can be manually imported into a
// series (season/episode parsed from the filename).
type SeriesImportCandidate struct {
	Path      string `json:"path"`
	Filename  string `json:"filename"`
	Season    int    `json:"season"`
	Episode   int    `json:"episode"`
	SizeBytes int64  `json:"size_bytes"`
	Quality   string `json:"quality"`
}

// SeriesImportCandidates lists importable video files under dir (recursively), up to
// library.ListMaxResults; truncated says there were more (or the walk was stopped).
// It stops early when ctx ends — the caller checks ctx.Err(). partly says some of dir
// couldn't be read (library.ErrPartlyUnreadable): the list is what the rest holds, and
// whoever picks from it should know there may be more.
func (c *Coordinator) SeriesImportCandidates(ctx context.Context, dir string) (cands []SeriesImportCandidate, truncated, partly bool) {
	vids, truncated, err := library.FindVideosCtx(ctx, dir, library.ListMaxResults, library.ListMaxVisited)
	partly = errors.Is(err, library.ErrPartlyUnreadable)
	out := make([]SeriesImportCandidate, 0, len(vids))
	for _, v := range vids {
		p := parser.Parse(filepath.Base(v.Path))
		ep := 0
		if len(p.Episodes) > 0 {
			ep = p.Episodes[0]
		}
		out = append(out, SeriesImportCandidate{
			Path: v.Path, Filename: filepath.Base(v.Path),
			Season: p.Season, Episode: ep, SizeBytes: v.Size, Quality: string(p.Resolution),
		})
	}
	return out, truncated, partly
}

// ManualImportSeries imports one on-disk file into a series as its parsed episode.
func (c *Coordinator) ManualImportSeries(ctx context.Context, seriesID int64, path string) error {
	if c.series == nil || c.imp == nil {
		return fmt.Errorf("series module not ready")
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return err
	}
	// A directory means "import this whole download", which runs the full pipeline —
	// quality inherited from the release name, the upgrade gate, multi-episode files —
	// rather than the single-file shortcut below.
	//
	// This is also the way to re-run an import Arrmada has already processed. The
	// automatic sweep won't revisit a download it recorded as imported unless the season
	// still has gaps, so when a fix changes what WOULD have imported, pointing manual
	// import at the folder is what applies it.
	if fi, statErr := os.Stat(path); statErr == nil && fi.IsDir() {
		if _, _, ferr := c.seriesImportPlacement(ctx, s); ferr != nil {
			return ferr
		}
		placedRefs, matched, unresolved, importFailed, conflict, scanErr := c.importSeriesInto(ctx, s, path, forceAll)
		if scanErr != nil {
			return errors.New(unreadableFolder(path, scanErr))
		}
		if conflict != "" {
			return errors.New(conflict) // nothing placed: its titles and numbers disagree
		}
		placed := len(placedRefs)
		if matched == 0 {
			return fmt.Errorf("none of the video files in that folder could be matched to an episode of %q", s.Title)
		}
		c.log.Info("series: manual folder import", "series", s.Title, "path", path,
			"placed", placed, "matched", matched, "unresolved", unresolved, "failed", importFailed)
		if placed > 0 {
			c.series.AddEvent(ctx, seriesID, "imported", fmt.Sprintf("Imported %d episode%s from %s", placed, plural(placed), filepath.Base(path)))
			c.seriesImported(ctx, seriesID, placedRefs)
		}
		// If the folder is a download the client still holds, record its hash as
		// handled (when nothing failed) — otherwise the 30s sweep re-scans it, places
		// nothing (everything now equal-or-better), and mis-blocklists the release
		// with "fully imported — no more episodes for this season".
		if importFailed == 0 {
			c.recordHashForContentPath(ctx, path, unresolved)
		}
		return nil
	}

	imp, folder, ferr := c.seriesImportPlacement(ctx, s)
	if ferr != nil {
		return ferr
	}
	ei, ok, err := imp.ImportEpisodeInto(folder, s.Title, s.Year, path)
	if err != nil {
		return err
	}
	if !ok {
		// Anime file numbered absolutely (no SxxExx) — resolve + place by absolute number.
		if s.IsAnime() && len(c.importAbsoluteEpisode(ctx, s, imp, folder, path)) > 0 {
			return nil
		}
		return fmt.Errorf("couldn't detect a season/episode from that filename")
	}
	var lastErr error
	for _, ep := range episodesOf(ei) { // double-episode file → mark both
		rs, re := c.series.ResolveEpisode(ctx, seriesID, ei.Season, ep)
		if err := c.series.SupersedeEpisodeFile(ctx, seriesID, rs, re, ei.TargetPath, ei.SizeBytes, filepath.Base(ei.SourcePath)); err != nil {
			lastErr = err
		}
	}
	return lastErr
}

// recordHashForContentPath finds the completed download whose content path matches,
// records its hash as imported and closes out its grab as 'imported', so seed cleanup
// removes the torrent at its goal. Used after manual imports, which are given a
// filesystem path rather than a torrent hash.
//
// unmatched is how many of its files the import couldn't place (noteImportUnmatched).
func (c *Coordinator) recordHashForContentPath(ctx context.Context, path string, unmatched int) {
	if c.downloads == nil {
		return
	}
	completed, err := c.downloads.CompletedInCategory(ctx, "")
	if err != nil {
		return
	}
	clean := filepath.Clean(path)
	for _, it := range completed {
		if it.ContentPath != "" && filepath.Clean(it.ContentPath) == clean {
			c.recordImportedHash(ctx, it.Hash, it.Name, it.SizeBytes)
			c.setGrabStatusByHash(ctx, it.Hash, it.Name, "series", grabStatusImported)
			c.noteImportUnmatched(ctx, it.Hash, it.Name, unmatched)
			return
		}
	}
}

// bestLibraryVideo picks which of several files claiming one episode to keep as that
// episode's file: highest resolution first, then the bigger file (higher bitrate at the
// same resolution), then the path — so the choice is deterministic rather than "whichever
// the directory walk yielded last".
func bestLibraryVideo(files []library.FoundVideo) library.FoundVideo {
	best := files[0]
	bestRank := parser.ResolutionRank(parser.Parse(filepath.Base(best.Path)).Resolution)
	for _, f := range files[1:] {
		rank := parser.ResolutionRank(parser.Parse(filepath.Base(f.Path)).Resolution)
		switch {
		case rank != bestRank:
			if rank > bestRank {
				best, bestRank = f, rank
			}
		case f.Size != best.Size:
			if f.Size > best.Size {
				best = f
			}
		case f.Path < best.Path:
			best = f
		}
	}
	return best
}

// DuplicateEpisodeFile is one episode that has more than one file on disk: the copy the
// library tracks, and the extra copies that are just taking up space.
type DuplicateEpisodeFile struct {
	Season  int             `json:"season"`
	Episode int             `json:"episode"`
	Keeping DuplicateCopy   `json:"keeping"`
	Extras  []DuplicateCopy `json:"extras"`
}

// DuplicateCopy is one file on disk claiming an episode.
type DuplicateCopy struct {
	Path      string `json:"path"`
	Filename  string `json:"filename"`
	SizeBytes int64  `json:"size_bytes"`
}

// SeriesDuplicates reports episodes with more than one file on disk.
//
// These accumulate because the library filename embeds derived metadata — the quality tag,
// and for a while the episode title. When that derivation changes (a source re-classified
// WEBRip vs WEB-DL, an episode title added or dropped) a re-import writes to a NEW path,
// and SupersedeEpisodeFile only recycles the ONE file the database was tracking. Every
// earlier copy is orphaned: invisible to Arrmada, still filling the disk, and showing up as
// a duplicate episode in Plex.
//
// Read-only — it reports what it finds and never deletes anything on its own.
func (c *Coordinator) SeriesDuplicates(ctx context.Context, seriesID int64) ([]DuplicateEpisodeFile, error) {
	if c.series == nil || c.imp == nil {
		return nil, nil
	}
	s, err := c.series.Get(ctx, seriesID)
	if err != nil {
		return nil, err
	}
	imp, folder, ferr := c.seriesPlacement(ctx, s)
	if ferr != nil {
		return nil, ferr
	}

	// Offered for deletion: only what a rescan would weigh as the episode's file
	// (episodeClaims) — never one it reads on the benefit of the doubt ("Other Show -
	// S01E01" in the show's own Season 1) or only as "1x8" (#81), nor another show's file
	// beside the show's own.
	recorded := recordedEpisodeFiles(s)
	byEpisode := map[[2]int][]library.FoundVideo{}
	holds := map[string][][2]int{} // each file's episodes, by cleaned path
	for _, v := range c.showEpisodeVideos(ctx, s, imp, folder) {
		for _, ref := range c.series.ResolveEpisodes(ctx, seriesID, library.ParseEpisodeFile(v.Path)) {
			key := [2]int{ref.Season, ref.Episode}
			byEpisode[key] = append(byEpisode[key], v)
			holds[filepath.Clean(v.Path)] = append(holds[filepath.Clean(v.Path)], key)
		}
	}

	// A file another episode has recorded (a two-episode file one of whose episodes now has
	// a better copy) is that episode's only file: never offered for deletion. Nor is a file
	// that also holds another episode, unless that episode has its own file, recorded and on
	// disk: "Show - S01E01E02" recorded for E2 alone is still E1's only copy.
	elsewhere := func(f library.FoundVideo, key [2]int) bool {
		for k, p := range recorded {
			if k != key && samePath(f.Path, p) {
				return true
			}
		}
		for _, k := range holds[filepath.Clean(f.Path)] {
			if k == key {
				continue
			}
			if p := recorded[k]; p == "" || samePath(p, f.Path) || fileGone(p) {
				return true
			}
		}
		return false
	}

	out := make([]DuplicateEpisodeFile, 0)
	for key, found := range byEpisode {
		files := episodeClaims(found, recorded[key], s)
		if len(files) < 2 {
			continue
		}
		best := keepOf(files)
		// A copy named as the show is never offered for deletion in favour of the recorded one
		// when its name isn't the show's ("S01E02.mkv", another show's file an earlier rescan
		// recorded, a romaji name): which is the episode's real file is for the owner to say.
		// Nor is that recorded file offered beside the show's own copy. Nor is anything
		// offered when the only evidence is the on-disk "1x8" reading (#81) of either name.
		named := func(f library.FoundVideo) bool { return namedAsShow(f, s) }
		if (!named(best) && slices.ContainsFunc(files, named)) || library.ReadOnlyOnDisk(best.Path) {
			continue
		}
		d := DuplicateEpisodeFile{Season: key[0], Episode: key[1], Keeping: copyOf(best)}
		for _, f := range files {
			if f.Path != best.Path && (named(f) || !named(best)) && !elsewhere(f, key) && !library.ReadOnlyOnDisk(f.Path) {
				d.Extras = append(d.Extras, copyOf(f))
			}
		}
		if len(d.Extras) > 0 {
			out = append(out, d)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Season != out[j].Season {
			return out[i].Season < out[j].Season
		}
		return out[i].Episode < out[j].Episode
	})
	return out, nil
}

// recordedEpisodeFiles is each episode's recorded file path.
func recordedEpisodeFiles(s series.Series) map[[2]int]string {
	out := map[[2]int]string{}
	for _, sn := range s.Seasons {
		for _, e := range sn.Episodes {
			if e.HasFile && e.FilePath != "" {
				out[[2]int{e.SeasonNumber, e.EpisodeNumber}] = e.FilePath
			}
		}
	}
	return out
}

// keptEpisodeFile picks which of the files on disk claiming one episode of show s, recorded
// as recorded, is kept, and the files that competed for it. The competitors are
// episodeClaims; with none, the files only fill an episode whose recorded file is gone (or
// that has none) — the likeliest to be the show's first (fillRank), then the best — and ok
// is false while the recorded file is still there: it stays.
func keptEpisodeFile(found []library.FoundVideo, recorded string, s series.Series) (best library.FoundVideo, files []library.FoundVideo, ok bool) {
	if files = episodeClaims(found, recorded, s); len(files) > 0 {
		return keepOf(files), files, true
	}
	if recorded != "" && !fileGone(recorded) {
		return library.FoundVideo{}, nil, false
	}
	files = found
	if len(found) > 1 {
		top := slices.MinFunc(found, func(a, b library.FoundVideo) int { return fillRank(a, s) - fillRank(b, s) })
		files = slices.DeleteFunc(slices.Clone(found), func(f library.FoundVideo) bool { return fillRank(f, s) != fillRank(top, s) })
	}
	return keepOf(files), files, true
}

// fillRank orders the files that may fill an episode with no file, likeliest the show's
// first: named as the show (0); named as the show but read only by the on-disk "1x8" rule
// (1); a name with no title ("S01E03.mkv", 2); named as something else — another show, or
// the show in a language it has no alias for (3); and last a file named otherwise that only
// the "1x8" rule reads (4).
func fillRank(f library.FoundVideo, s series.Series) int {
	onDisk := library.ReadOnlyOnDisk(f.Path)
	switch {
	case series.FileFitsShow(f.Path, s):
		if onDisk {
			return 1
		}
		return 0
	case onDisk:
		return 4
	case strings.TrimSpace(parser.Parse(filepath.Base(f.Path)).Title) == "":
		return 2
	default:
		return 3
	}
}

// episodeClaims is the files of one episode that may be kept as its file or offered as its
// duplicates: those named as the show (namedAsShow: read by their name, not only by the
// on-disk "1x8" rule, #81), and the one the episode has recorded — unless that one is read
// only as "1x8" and a file named as the show is there: a file read by its name always wins
// over it. The rest — read on the benefit of the doubt ("Other Show - 1x08" in the show's
// own Season 1, "S01E08.mkv"), only as "1x8", or recorded as another episode ("Other Show -
// S01E02E03" an earlier rescan gave E03) — never displace the recorded file.
func episodeClaims(found []library.FoundVideo, recorded string, s series.Series) []library.FoundVideo {
	var out []library.FoundVideo
	var rec []library.FoundVideo
	for _, f := range found {
		switch {
		case recorded != "" && samePath(f.Path, recorded):
			rec = append(rec, f)
		case namedAsShow(f, s):
			out = append(out, f)
		}
	}
	for _, f := range rec {
		if len(out) == 0 || !library.ReadOnlyOnDisk(f.Path) {
			out = append(out, f)
		}
	}
	return out
}

// namedAsShow reports whether f's name says it's show s's (series.FileFitsShow) as a release
// name would: a name read only by the on-disk "1x8" rule (library.ReadOnlyOnDisk) isn't.
func namedAsShow(f library.FoundVideo, s series.Series) bool {
	return series.FileFitsShow(f.Path, s) && !library.ReadOnlyOnDisk(f.Path)
}

// keepOf is the file kept of files (one or more): the best (bestLibraryVideo).
func keepOf(files []library.FoundVideo) library.FoundVideo {
	if len(files) == 1 {
		return files[0]
	}
	return bestLibraryVideo(files)
}

// samePath reports whether a and b are the same file: equal once cleaned, or differing only
// in letter case and one directory entry on disk (library.SamePlace) — as on Windows, macOS
// or an SMB share, which ignore case. On a disk that doesn't, two such names are two files
// (or one isn't there), so they stay different. Only a case-only difference touches the disk.
func samePath(a, b string) bool {
	a, b = filepath.Clean(a), filepath.Clean(b)
	return a == b || (strings.EqualFold(a, b) && library.SamePlace(a, b))
}

func copyOf(v library.FoundVideo) DuplicateCopy {
	return DuplicateCopy{Path: v.Path, Filename: filepath.Base(v.Path), SizeBytes: v.Size}
}

// DeleteSeriesDuplicate recycles one duplicate file. It refuses any path that isn't
// currently reported as an EXTRA copy for this series, so an arbitrary path — or the file
// the library actually tracks — can never be deleted through this route.
func (c *Coordinator) DeleteSeriesDuplicate(ctx context.Context, seriesID int64, path string) error {
	dupes, err := c.SeriesDuplicates(ctx, seriesID)
	if err != nil {
		return err
	}
	for _, d := range dupes {
		for _, e := range d.Extras {
			if e.Path != path {
				continue
			}
			// Into the bin, or for good only when the bin is deliberately off. A bin that
			// refuses the file leaves it in place and says why. The path is the scan's
			// (equal to the request's), so what's removed is always a file the scan found.
			if _, err := library.RemoveToBin(c.bin(), e.Path); err != nil {
				return err
			}
			c.log.Info("series: deleted a duplicate episode file", "series", seriesID,
				"episode", fmt.Sprintf("S%02dE%02d", d.Season, d.Episode), "path", e.Path)
			c.series.AddEvent(ctx, seriesID, "file.deleted",
				fmt.Sprintf("Deleted duplicate file for S%02dE%02d: %s", d.Season, d.Episode, filepath.Base(e.Path)))
			return nil
		}
	}
	return fmt.Errorf("that file isn't a duplicate of this series — refusing to delete it")
}

// resolvedLabel says which episodes a release actually maps to, for the diagnostic that
// explains an out-of-scope drop. Best-effort and read-only.
func (c *Coordinator) resolvedLabel(ctx context.Context, s series.Series, p parser.Release) string {
	refs, ok := c.series.AliasEpisodes(ctx, s.ID, p)
	via := "alias"
	if !ok {
		refs, via = c.series.ResolveEpisodes(ctx, s.ID, p), "series numbering"
	}
	if len(refs) == 0 {
		return "no episode could be resolved"
	}
	parts := make([]string, 0, 4)
	for i, ref := range refs {
		if i == 4 {
			parts = append(parts, fmt.Sprintf("+%d more", len(refs)-4))
			break
		}
		parts = append(parts, fmt.Sprintf("S%02dE%02d", ref.Season, ref.Episode))
	}
	return strings.Join(parts, ",") + " (via " + via + ")"
}

// resolvesLabel names the library episode(s) a release maps to, for the search list.
// Terser than resolvedLabel: no "via" attribution, and a range rather than a list, since
// this sits in a row rather than a log line.
func (c *Coordinator) resolvesLabel(ctx context.Context, s series.Series, p parser.Release) string {
	refs, ok := c.series.AliasEpisodes(ctx, s.ID, p)
	if !ok {
		refs = c.series.ResolveEpisodes(ctx, s.ID, p)
	}
	if len(refs) == 0 {
		return ""
	}
	first := refs[0]
	if len(refs) == 1 {
		return fmt.Sprintf("S%02dE%02d", first.Season, first.Episode)
	}
	last := refs[len(refs)-1]
	return fmt.Sprintf("S%02dE%02d–E%02d (%d episodes)", first.Season, first.Episode, last.Episode, len(refs))
}
