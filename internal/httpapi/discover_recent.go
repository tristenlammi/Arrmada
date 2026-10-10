package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/adultfilter"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/metadata"
)

// "Recently added": what just arrived in the library, for every role. The source is
// Arrmada's own import history, so the row fills with Plex disconnected; when the Plex
// index is built, Plex's own recently added list is merged in (mapped to TMDB ids), so a
// title the owner added outside Arrmada shows up too. Nothing about Plex leaves the
// server: no library names, paths or users, only the titles as cards.

const (
	recentDays       = 30              // how far back an import counts as recent
	recentMax        = 40              // cards in the row
	recentPlexScan   = 60              // Plex rows read before mapping
	recentLookups    = 12              // TMDB lookups for Plex titles Arrmada doesn't manage
	recentTTL        = 2 * time.Minute // the row is the same for everyone, so it's shared
	recentPlexBudget = 6 * time.Second // Plex and the lookups together; the row never waits longer
)

type recentEntry struct {
	at    time.Time
	items []metadata.DiscoverItem
}

var recentCache sync.Map // *api -> *recentEntry

// recentTitle is one candidate card with when it arrived (unix seconds).
type recentTitle struct {
	item metadata.DiscoverItem
	at   int64
}

func (a *api) handleDiscoverRecentlyAdded(w http.ResponseWriter, r *http.Request) {
	if v, ok := recentCache.Load(a); ok {
		if e := v.(*recentEntry); time.Since(e.at) < recentTTL {
			a.enrichDiscover(w, r, e.items)
			return
		}
	}
	items, complete, err := a.recentlyAdded(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list recent imports")
		return
	}
	// A row built while Plex was slow is served but not kept, so the next open tries again.
	if complete {
		recentCache.Store(a, &recentEntry{at: time.Now(), items: items})
	}
	a.enrichDiscover(w, r, items)
}

// recentlyAdded builds the row. complete is false when the Plex half was skipped for a
// failure or ran out of time.
func (a *api) recentlyAdded(ctx context.Context) ([]metadata.DiscoverItem, bool, error) {
	var local []recentTitle
	if a.deps.Movies != nil {
		ms, err := a.deps.Movies.RecentlyImported(ctx, recentDays, recentMax)
		if err != nil {
			return nil, false, err
		}
		for _, m := range ms {
			local = append(local, recentTitle{at: m.At, item: metadata.DiscoverItem{
				MediaType: "movie", TMDBID: m.TMDBID, Title: m.Title, Year: m.Year, Overview: m.Overview, PosterURL: m.PosterURL,
			}})
		}
	}
	if a.deps.Series != nil {
		ss, err := a.deps.Series.RecentlyImported(ctx, recentDays, recentMax)
		if err != nil {
			return nil, false, err
		}
		for _, s := range ss {
			local = append(local, recentTitle{at: s.At, item: metadata.DiscoverItem{
				MediaType: "series", TMDBID: s.TMDBID, Title: s.Title, Year: s.Year, Overview: s.Overview, PosterURL: s.PosterURL,
			}})
		}
	}
	plexRecent, complete := a.plexRecent(ctx)
	return mergeRecent(local, plexRecent, a.recentLookup(ctx), time.Now().AddDate(0, 0, -recentDays).Unix()), complete, nil
}

// plexRecent is Plex's recently added titles by TMDB id, or nothing when Plex isn't set
// up, its index isn't built, or it doesn't answer in time.
func (a *api) plexRecent(ctx context.Context) ([]insights.RecentTMDB, bool) {
	if a.deps.Insights == nil || !a.plexReady(ctx) {
		return nil, true
	}
	pctx, cancel := context.WithTimeout(ctx, recentPlexBudget)
	defer cancel()
	items, err := a.deps.Insights.RecentlyAddedTMDB(pctx, recentPlexScan)
	if err != nil {
		return nil, false
	}
	return items, true
}

// recentLookup finds the card for a Plex title: from the library when Arrmada has it,
// else from TMDB (the detail record is disk-cached), at most recentLookups times per
// build. A title TMDB flags adult is never returned.
func (a *api) recentLookup(ctx context.Context) func(media string, tmdb int) (metadata.DiscoverItem, bool) {
	var movies, shows map[int]metadata.DiscoverItem
	lookups := 0
	deadline := time.Now().Add(recentPlexBudget)
	return func(media string, tmdb int) (metadata.DiscoverItem, bool) {
		if movies == nil {
			movies, shows = map[int]metadata.DiscoverItem{}, map[int]metadata.DiscoverItem{}
			if a.deps.Movies != nil {
				if ms, err := a.deps.Movies.List(ctx); err == nil {
					for _, m := range ms {
						if m.HasFile {
							movies[m.TMDBID] = metadata.DiscoverItem{MediaType: "movie", TMDBID: m.TMDBID, Title: m.Title, Year: m.Year, Overview: m.Overview, PosterURL: m.PosterURL}
						}
					}
				}
			}
			if a.deps.Series != nil {
				if ss, err := a.deps.Series.List(ctx); err == nil {
					for _, s := range ss {
						shows[s.TMDBID] = metadata.DiscoverItem{MediaType: "series", TMDBID: s.TMDBID, Title: s.Title, Year: s.Year, Overview: s.Overview, PosterURL: s.PosterURL}
					}
				}
			}
		}
		lib := movies
		if media == "series" {
			lib = shows
		}
		if it, ok := lib[tmdb]; ok {
			return it, true
		}
		if !a.metadataReady() || lookups >= recentLookups || time.Now().After(deadline) {
			return metadata.DiscoverItem{}, false
		}
		lookups++
		lctx, cancel := context.WithDeadline(ctx, deadline)
		defer cancel()
		d, err := a.deps.Discovery.MediaDetails(lctx, media, tmdb)
		if err != nil || d == nil || d.Adult {
			return metadata.DiscoverItem{}, false
		}
		return itemFromDetail(d), true
	}
}

// mergeRecent joins Arrmada's recent imports with Plex's recently added titles: one card
// per title (its newest arrival), newest first, nothing older than since, no adult titles
// and no posterless cards, at most recentMax.
func mergeRecent(local []recentTitle, plexItems []insights.RecentTMDB, lookup func(media string, tmdb int) (metadata.DiscoverItem, bool), since int64) []metadata.DiscoverItem {
	byKey := map[string]int{}
	var all []recentTitle
	add := func(rt recentTitle) {
		it := rt.item
		if it.TMDBID <= 0 || it.Title == "" || it.PosterURL == "" || adultfilter.Matches(it.Title) {
			return
		}
		key := it.MediaType + ":" + strconv.Itoa(it.TMDBID)
		if i, ok := byKey[key]; ok {
			if rt.at > all[i].at {
				all[i].at = rt.at
			}
			return
		}
		byKey[key] = len(all)
		all = append(all, rt)
	}
	for _, rt := range local {
		add(rt)
	}
	for _, p := range plexItems {
		if p.AddedAt < since {
			continue
		}
		key := p.Media + ":" + strconv.Itoa(p.TMDB)
		if i, ok := byKey[key]; ok {
			if p.AddedAt > all[i].at {
				all[i].at = p.AddedAt
			}
			continue
		}
		if lookup == nil {
			continue
		}
		if it, ok := lookup(p.Media, p.TMDB); ok {
			add(recentTitle{item: it, at: p.AddedAt})
		}
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].at > all[j].at })
	out := make([]metadata.DiscoverItem, 0, min(len(all), recentMax))
	for _, rt := range all {
		if len(out) >= recentMax {
			break
		}
		out = append(out, rt.item)
	}
	return out
}
