package httpapi

import (
	"context"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/series"
)

// The extra Discover rows: the TMDB-backed ones (in cinemas, top rated, anime, hidden
// gems, your region, new on a streaming service), the per-seed "Because you watched
// …" strips, and "Finish your collections" from the movie library.

func (a *api) discoverRows() (metadata.DiscoverRows, bool) {
	if a.deps.Discovery == nil || !a.deps.Discovery.Available() {
		return nil, false
	}
	r, ok := a.deps.Discovery.(metadata.DiscoverRows)
	return r, ok
}

// handleDiscoverRow serves one named row; an unknown kind is a 404 so the page hides it.
func (a *api) handleDiscoverRow(w http.ResponseWriter, r *http.Request) {
	rows, ok := a.discoverRows()
	if !ok {
		a.writeError(w, http.StatusNotFound, "not available")
		return
	}
	media := r.URL.Query().Get("media")
	ctx := r.Context()
	var items []metadata.DiscoverItem
	var err error
	switch r.PathValue("kind") {
	case "now_playing":
		items, err = rows.NowPlaying(ctx)
	case "top_rated":
		items, err = rows.TopRated(ctx, media)
	case "anime":
		items, err = rows.Anime(ctx)
	case "hidden_gems":
		items, err = rows.HiddenGems(ctx, media)
	case "region":
		items, err = rows.FromRegion(ctx, media)
	default:
		a.writeError(w, http.StatusNotFound, "unknown row")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.enrichDiscover(w, r, items)
}

// handleDiscoverProviders lists the region's streaming services for the chips.
func (a *api) handleDiscoverProviders(w http.ResponseWriter, r *http.Request) {
	rows, ok := a.discoverRows()
	if !ok {
		a.writeJSON(w, http.StatusOK, map[string]any{"providers": []metadata.WatchProvider{}})
		return
	}
	ps, err := rows.WatchProviders(r.Context(), r.URL.Query().Get("media"))
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if ps == nil {
		ps = []metadata.WatchProvider{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"providers": ps})
}

// handleDiscoverProviderNew is "new on <service>".
func (a *api) handleDiscoverProviderNew(w http.ResponseWriter, r *http.Request) {
	rows, ok := a.discoverRows()
	if !ok {
		a.writeError(w, http.StatusNotFound, "not available")
		return
	}
	id, _ := strconv.Atoi(r.URL.Query().Get("id"))
	if id <= 0 {
		a.writeError(w, http.StatusBadRequest, "provider id is required")
		return
	}
	items, err := rows.NewOnProvider(r.Context(), r.URL.Query().Get("media"), id)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.enrichDiscover(w, r, items)
}

// --- "Because you watched X" ---

const becauseRows = 2

type discoverRow struct {
	Title string         `json:"title"`
	Seed  string         `json:"seed"`
	Items []discoverCard `json:"items"`
}

type titledSeed struct {
	seed
	title string
	verb  string // "watched" | "requested"
}

// becauseSeeds are the viewer's two most recent titles with a name attached: what
// they last watched (Plex, matched to the library), then what they last requested.
func (a *api) becauseSeeds(ctx context.Context, userID int64) []titledSeed {
	var out []titledSeed
	seen := map[string]bool{}
	add := func(media string, tmdb int, title, verb string) {
		key := media + ":" + strconv.Itoa(tmdb)
		if tmdb <= 0 || title == "" || seen[key] {
			return
		}
		seen[key] = true
		out = append(out, titledSeed{seed: seed{media: media, tmdb: tmdb}, title: title, verb: verb})
	}
	if a.deps.Insights != nil && a.deps.Auth != nil {
		if plexID := a.deps.Auth.PlexIDForUser(ctx, userID); plexID != "" {
			if watched, err := a.deps.Insights.RecentlyWatchedByUser(ctx, plexID, recWatchSeedScan); err == nil {
				for _, wt := range watched {
					if len(out) >= becauseRows {
						break
					}
					if wt.MediaType == "episode" || wt.MediaType == "show" {
						if a.deps.Series != nil {
							if sr, ok := a.deps.Series.MatchByTitle(ctx, series.NormTitle(wt.Title)); ok {
								add("series", sr.TMDBID, sr.Title, "watched")
							}
						}
					} else if a.deps.Movies != nil {
						if m, ok := a.deps.Movies.Match(ctx, wt.Title, wt.Year); ok {
							add("movie", m.TMDBID, m.Title, "watched")
						}
					}
				}
			}
		}
	}
	if a.deps.Requests != nil && len(out) < becauseRows {
		if reqs, err := a.deps.Requests.Records(ctx, requests.ListFilter{UserID: userID, IncludeJoined: true, Limit: recReqSeedScan}); err == nil {
			for i, rq := range reqs {
				if i >= recReqSeedScan || len(out) >= becauseRows {
					break
				}
				if rq.MediaType == "movie" || rq.MediaType == "series" {
					add(rq.MediaType, rq.TMDBID, rq.Title, "requested")
				}
			}
		}
	}
	return out
}

// handleDiscoverBecause returns up to two per-seed rows, each the recommendations for
// one title the viewer engaged with, minus what they already have or asked for.
func (a *api) handleDiscoverBecause(w http.ResponseWriter, r *http.Request) {
	out := []discoverRow{}
	u, ok := userFrom(r)
	if !ok || u == nil || a.deps.Discovery == nil || !a.deps.Discovery.Available() {
		a.writeJSON(w, http.StatusOK, map[string]any{"rows": out})
		return
	}
	ctx := r.Context()
	for _, s := range a.becauseSeeds(ctx, u.ID) {
		recs, err := a.deps.Discovery.Recommendations(ctx, s.media, s.tmdb)
		if err != nil || len(recs) == 0 {
			continue
		}
		cards := a.enrichCards(ctx, recs)
		items := make([]discoverCard, 0, recResultCap)
		for _, c := range cards {
			if c.InLibrary || c.RequestStatus == "pending" || c.RequestStatus == "approved" {
				continue
			}
			items = append(items, c)
			if len(items) >= recResultCap {
				break
			}
		}
		if len(items) < 4 {
			continue
		}
		out = append(out, discoverRow{Title: "Because you " + s.verb + " " + s.title, Seed: s.title, Items: items})
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"rows": out})
}

// --- "Finish your collections" ---

const (
	collectionsMax    = 8
	collectionsCap    = 30
	collectionsTTL    = 6 * time.Hour
	collectionMinOwn  = 1
	collectionFetcher = "collection"
)

type collectionsCacheEntry struct {
	at    time.Time
	items []metadata.DiscoverItem
}

var collectionsCache sync.Map // *api -> *collectionsCacheEntry

// handleDiscoverCollections lists the members the library lacks from movie collections
// it has started — the sequel you never got, the original behind the one you have.
func (a *api) handleDiscoverCollections(w http.ResponseWriter, r *http.Request) {
	if a.deps.Movies == nil || a.deps.Discovery == nil || !a.deps.Discovery.Available() {
		a.enrichDiscover(w, r, nil)
		return
	}
	ctx := r.Context()
	if v, ok := collectionsCache.Load(a); ok {
		if e := v.(*collectionsCacheEntry); time.Since(e.at) < collectionsTTL {
			a.enrichDiscover(w, r, e.items)
			return
		}
	}
	getter, ok := a.deps.Discovery.(interface {
		GetCollection(ctx context.Context, id int) (*metadata.Collection, error)
	})
	if !ok {
		a.enrichDiscover(w, r, nil)
		return
	}
	movies, err := a.deps.Movies.List(ctx)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list movies")
		return
	}
	owned := map[int]bool{}
	ownedPerColl := map[int]int{}
	names := map[int]string{}
	for _, m := range movies {
		owned[m.TMDBID] = true
		if m.Extra != nil && m.Extra.CollectionID > 0 {
			ownedPerColl[m.Extra.CollectionID]++
			names[m.Extra.CollectionID] = m.Extra.CollectionName
		}
	}
	// Collections the library has started, most-complete first (those are the ones
	// worth finishing), capped so this stays a few requests.
	var ids []int
	for id, n := range ownedPerColl {
		if n >= collectionMinOwn {
			ids = append(ids, id)
		}
	}
	sort.Slice(ids, func(i, j int) bool {
		if ownedPerColl[ids[i]] != ownedPerColl[ids[j]] {
			return ownedPerColl[ids[i]] > ownedPerColl[ids[j]]
		}
		return names[ids[i]] < names[ids[j]]
	})
	if len(ids) > collectionsMax {
		ids = ids[:collectionsMax]
	}
	var items []metadata.DiscoverItem
	for _, id := range ids {
		c, err := getter.GetCollection(ctx, id)
		if err != nil {
			continue
		}
		for _, m := range c.Members {
			if owned[m.TMDBID] || m.PosterURL == "" || m.Year == 0 || m.Year > time.Now().Year() {
				continue // unreleased members belong on Upcoming, not here
			}
			items = append(items, metadata.DiscoverItem{
				MediaType: "movie", TMDBID: m.TMDBID, Title: m.Title, Year: m.Year,
				Overview: m.Overview, PosterURL: m.PosterURL, VoteAverage: m.VoteAverage,
			})
			if len(items) >= collectionsCap {
				break
			}
		}
		if len(items) >= collectionsCap {
			break
		}
	}
	collectionsCache.Store(a, &collectionsCacheEntry{at: time.Now(), items: items})
	a.enrichDiscover(w, r, items)
}
