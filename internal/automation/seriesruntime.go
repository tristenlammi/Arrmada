package automation

import (
	"context"
	"sort"

	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/parser"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/series"
)

// runtimeIndex is a show's episode lengths, arranged so a release's runtime can be summed
// from whatever it covers. The quality profile's bitrate window turns size into Mb/s only
// when a candidate carries a runtime; series candidates never did, so a 25 Mb/s BluRay beat
// a 5 Mb/s WEB-DL under a "1080p 5–15 Mb/s" profile and upgrades climbed past the ceiling.
type runtimeIndex struct {
	byEp map[epKey]int
	// typical stands in for an episode TMDB lists without a length: the median of the
	// lengths it does list, which is close for nearly every show.
	typical int
	// present is every episode that has aired or has a file — what a pack can actually
	// hold. A season pack of a running show doesn't contain next month's episodes, and
	// counting them would make the pack look leaner than it is.
	present   map[epKey]bool
	seasonEps map[int][]epKey
}

func newRuntimeIndex(s series.Series) runtimeIndex {
	idx := runtimeIndex{byEp: map[epKey]int{}, present: map[epKey]bool{}, seasonEps: map[int][]epKey{}}
	var lengths []int
	for _, sn := range s.Seasons {
		for _, e := range sn.Episodes {
			k := epKey{e.SeasonNumber, e.EpisodeNumber}
			idx.byEp[k] = e.Runtime
			if e.Runtime > 0 {
				lengths = append(lengths, e.Runtime)
			}
			if e.HasFile || aired(e.AirDate) {
				idx.present[k] = true
				if e.SeasonNumber > 0 {
					idx.seasonEps[e.SeasonNumber] = append(idx.seasonEps[e.SeasonNumber], k)
				}
			}
		}
	}
	if len(lengths) > 0 {
		sort.Ints(lengths)
		idx.typical = lengths[len(lengths)/2]
	}
	return idx
}

// sum is the total length of the given episodes, filling unknown lengths with the typical
// one. 0 when there's nothing to sum or no length is known at all — the ceiling then
// doesn't apply, exactly as before series candidates carried a runtime.
func (idx runtimeIndex) sum(keys []epKey) int {
	total := 0
	for _, k := range keys {
		rt := idx.byEp[k]
		if rt <= 0 {
			rt = idx.typical
		}
		if rt <= 0 {
			return 0
		}
		total += rt
	}
	return total
}

// onlyPresent narrows resolved pack episodes to the ones that have aired or have a file.
func (idx runtimeIndex) onlyPresent(keys []epKey) []epKey {
	out := keys[:0:0]
	for _, k := range keys {
		if idx.present[k] {
			out = append(out, k)
		}
	}
	return out
}

// releaseRuntime is how many minutes of the show a release holds, resolved to episodes the
// same way coveredByFor decides what it covers: through an alias' own numbering first, then
// anime absolute and split-season mapping, then plain SxxExx, and for packs every aired
// episode of the seasons they cover.
func (c *Coordinator) releaseRuntime(ctx context.Context, s series.Series, idx runtimeIndex, r parser.Release) int {
	return idx.sum(c.releaseEpisodes(ctx, s, idx, r))
}

func (c *Coordinator) releaseEpisodes(ctx context.Context, s series.Series, idx runtimeIndex, r parser.Release) []epKey {
	if !r.IsTV() {
		return nil
	}
	if c.series != nil {
		if refs, ok := c.series.AliasEpisodes(ctx, s.ID, r); ok {
			keys := refKeys(refs)
			if r.Kind() != parser.KindEpisode {
				keys = idx.onlyPresent(keys) // a whole-cour pack: only what has aired
			}
			return keys
		}
		if s.IsAnime() {
			switch {
			case r.Kind() == parser.KindEpisode:
				return refKeys(c.series.ResolveEpisodes(ctx, s.ID, r))
			case r.Season > 0 && !r.Complete && len(r.Seasons) <= 1 && !c.series.HasSeason(ctx, s.ID, r.Season):
				return idx.onlyPresent(refKeys(c.series.SceneSeasonEpisodes(ctx, s.ID, r.Season)))
			}
		}
	}
	if r.Kind() == parser.KindEpisode {
		keys := make([]epKey, 0, len(r.Episodes))
		for _, e := range r.Episodes {
			keys = append(keys, epKey{r.Season, e})
		}
		return keys
	}
	// A season pack, several seasons or the whole show. Seasons in order, so the sum (and
	// any test of it) is deterministic.
	seasons := make([]int, 0, len(idx.seasonEps))
	for sn := range idx.seasonEps {
		if r.CoversSeason(sn) {
			seasons = append(seasons, sn)
		}
	}
	sort.Ints(seasons)
	var keys []epKey
	for _, sn := range seasons {
		keys = append(keys, idx.seasonEps[sn]...)
	}
	return keys
}

func refKeys(refs []series.EpisodeRef) []epKey {
	out := make([]epKey, 0, len(refs))
	for _, ref := range refs {
		out = append(out, epKey{ref.Season, ref.Episode})
	}
	return out
}

// episodeUpgradeCandidates lists the releases that could replace one episode's file —
// single- or multi-episode releases that contain it — each carrying the runtime it covers,
// so an S01E01E02 double is costed over both episodes and the ceiling holds for upgrades
// too. Sorted by name so the pick doesn't depend on map order.
func (c *Coordinator) episodeUpgradeCandidates(ctx context.Context, s series.Series, idx runtimeIndex, byName map[string]indexer.Release, season, episode int) []quality.Candidate {
	var cands []quality.Candidate
	for name, rel := range byName {
		if episodeRelease(parser.Parse(name), season, episode) {
			cands = append(cands, c.newSeriesCandidate(ctx, s, idx, rel))
		}
	}
	sort.Slice(cands, func(i, j int) bool { return cands[i].Name < cands[j].Name })
	return cands
}

// newSeriesCandidate turns an indexer release into a scoring candidate carrying the
// runtime it covers, so the profile's bitrate window applies to TV exactly as it does to a
// movie. Every TV path — the sweep, RSS, the quick buttons, the interactive list and the
// upgrade sweep — builds candidates here, so a release scores the same whichever one looks
// at it. Build idx once per pass with newRuntimeIndex.
func (c *Coordinator) newSeriesCandidate(ctx context.Context, s series.Series, idx runtimeIndex, rel indexer.Release) quality.Candidate {
	cand := quality.NewCandidate(rel.Title, rel.SizeGB(), rel.Seeders)
	return cand.WithRuntime(c.releaseRuntime(ctx, s, idx, cand.Release))
}
