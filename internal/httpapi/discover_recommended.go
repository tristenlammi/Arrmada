package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/series"
)

// Personalized "Recommended for you" row.
//
// It answers a specific question: given what this user has watched (Plex history via
// Insights) and requested, what NEW titles would they likely want? The chain is:
//
//	seeds (the user's recent requests + recently-watched titles matched to the library
//	for their TMDB ids) → TMDB recommendations per seed → aggregate by how many seeds
//	surface each title (and their recency) → drop what they already have or requested →
//	rank and enrich.
//
// Everything is per-user and needs the session, so it can't be globally cached — but
// the expensive part (TMDB recommendation calls) IS cached per seed by the discovery
// provider, so the per-request work is just DB reads and map merges.

const (
	recSeedCap       = 12 // most-recent unique seeds to fan out from
	recWatchSeedScan = 25 // watch-history rows to consider (before library matching)
	recReqSeedScan   = 20 // recent requests to consider
	recResultCap     = 20 // titles returned in the row
)

// seed is one title the recommendations are built from.
type seed struct {
	media string // "movie" | "series"
	tmdb  int
}

func (a *api) handleDiscoverRecommended(w http.ResponseWriter, r *http.Request) {
	if a.deps.Discovery == nil || !a.deps.Discovery.Available() {
		a.writeJSON(w, http.StatusOK, map[string]any{"items": []discoverCard{}})
		return
	}
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"items": []discoverCard{}})
		return
	}
	ctx := r.Context()

	seeds, seedSet := a.recommendationSeeds(ctx, u.ID)
	if len(seeds) == 0 {
		// No history to personalize from — the frontend hides the row.
		a.writeJSON(w, http.StatusOK, map[string]any{"items": []discoverCard{}})
		return
	}

	// Fetch each seed's recommendations (cached per seed by the provider), then rank.
	perSeed := make([][]metadata.DiscoverItem, len(seeds))
	for i, s := range seeds {
		if recs, err := a.deps.Discovery.Recommendations(ctx, s.media, s.tmdb); err == nil {
			perSeed[i] = recs // a failed seed contributes nothing rather than sinking the row
		}
	}
	ranked := rankRecommendations(perSeed, seedSet)
	if len(ranked) == 0 {
		a.writeJSON(w, http.StatusOK, map[string]any{"items": []discoverCard{}})
		return
	}
	cards := a.enrichCards(ctx, ranked)

	// Recommend things they DON'T already have or have in flight: drop in-library and
	// anything already requested. This is the payoff of enriching before filtering.
	out := make([]discoverCard, 0, recResultCap)
	for _, c := range cards {
		if c.InLibrary || c.RequestStatus == "pending" || c.RequestStatus == "approved" {
			continue
		}
		out = append(out, c)
		if len(out) >= recResultCap {
			break
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": out})
}

// rankRecommendations aggregates per-seed recommendation lists into one ranked list.
// perSeed is ordered most-recent-seed-first. A title recommended by several seeds
// outranks one from a single seed; a more recent seed weighs slightly more; a title
// that is itself one of the user's seeds is never returned. Ties break on TMDB rating.
func rankRecommendations(perSeed [][]metadata.DiscoverItem, seedSet map[string]bool) []metadata.DiscoverItem {
	type scored struct {
		item  metadata.DiscoverItem
		score float64
	}
	n := len(perSeed)
	byKey := map[string]int{}
	var list []scored
	for i, recs := range perSeed {
		weight := 1.0 + float64(n-i)/float64(n) // 2.0 for the most recent seed → ~1.0 for the oldest
		for _, it := range recs {
			key := it.MediaType + ":" + strconv.Itoa(it.TMDBID)
			if seedSet[key] {
				continue
			}
			if idx, seen := byKey[key]; seen {
				list[idx].score += weight
			} else {
				byKey[key] = len(list)
				list = append(list, scored{item: it, score: weight})
			}
		}
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return list[i].item.VoteAverage > list[j].item.VoteAverage
	})
	out := make([]metadata.DiscoverItem, len(list))
	for i, s := range list {
		out[i] = s.item
	}
	return out
}

// recommendationSeeds gathers this user's seed titles — recent requests plus
// recently-watched titles matched to the library — newest first, deduped, capped.
func (a *api) recommendationSeeds(ctx context.Context, userID int64) ([]seed, map[string]bool) {
	set := map[string]bool{}
	var seeds []seed
	add := func(media string, tmdb int) {
		if tmdb <= 0 {
			return
		}
		key := media + ":" + strconv.Itoa(tmdb)
		if set[key] {
			return
		}
		set[key] = true
		seeds = append(seeds, seed{media: media, tmdb: tmdb})
	}

	// Watch history first (newest interests), if the user is Plex-linked. Match each
	// watched title back to the library to recover its TMDB id — Plex sessions store
	// only Plex's internal rating_key, but a watched title is almost always in the
	// library, which carries the TMDB id.
	if a.deps.Insights != nil && a.deps.Auth != nil {
		if plexID := a.deps.Auth.PlexIDForUser(ctx, userID); plexID != "" {
			if watched, err := a.deps.Insights.RecentlyWatchedByUser(ctx, plexID, recWatchSeedScan); err == nil {
				for _, wt := range watched {
					if len(seeds) >= recSeedCap {
						break
					}
					if wt.MediaType == "episode" || wt.MediaType == "show" {
						if a.deps.Series != nil {
							if sr, ok := a.deps.Series.MatchByTitle(ctx, series.NormTitle(wt.Title)); ok {
								add("series", sr.TMDBID)
							}
						}
					} else if a.deps.Movies != nil {
						if m, ok := a.deps.Movies.Match(ctx, wt.Title, wt.Year); ok {
							add("movie", m.TMDBID)
						}
					}
				}
			}
		}
	}

	// Then recent requests (explicit intent), which carry the TMDB id directly.
	if a.deps.Requests != nil && len(seeds) < recSeedCap {
		if reqs, err := a.deps.Requests.List(ctx, "", userID); err == nil {
			for i, rq := range reqs {
				if i >= recReqSeedScan || len(seeds) >= recSeedCap {
					break
				}
				if rq.MediaType == "movie" || rq.MediaType == "series" {
					add(rq.MediaType, rq.TMDBID)
				}
			}
		}
	}
	return seeds, set
}
