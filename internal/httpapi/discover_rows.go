package httpapi

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/series"
)

// The extra Discover rows: the TMDB-backed ones (in cinemas, top rated, anime, hidden
// gems, your region, new on a streaming service), the per-seed "Because you watched
// …" strips, and the "Complete the X collection" rows from the movie library.

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

// --- "Complete the X collection" ---

const (
	collectionsRowsMax   = 4  // rows on Discover
	collectionsFetchMax  = 12 // started collections looked at to find them (each cached 6 h)
	collectionsTTL       = 6 * time.Hour
	collectionMinMissing = 2 // a row needs at least two released films still to get
)

// collectionRow is one "Complete the <name>" row.
type collectionRow struct {
	CollectionID int            `json:"collection_id"`
	Title        string         `json:"title"`
	Items        []discoverCard `json:"items"`
}

// collectionRowItems is a row before the viewer's badges are added (what's cached).
type collectionRowItems struct {
	id    int
	title string
	items []metadata.DiscoverItem
}

type collectionsCacheEntry struct {
	at   time.Time
	rows []collectionRowItems
}

var collectionsCache sync.Map // *api -> *collectionsCacheEntry

type collectionGetter interface {
	GetCollection(ctx context.Context, id int) (*metadata.Collection, error)
}

// releasedBy reports whether a collection member is out on the given day (YYYY-MM-DD):
// by its release date, or by its year when TMDB has no date. Unreleased members belong
// on Upcoming, not in "still to get".
func releasedBy(m metadata.MovieResult, today string) bool {
	if m.ReleaseDate != "" {
		return m.ReleaseDate <= today
	}
	return m.Year > 0 && strconv.Itoa(m.Year) < today[:4]
}

func memberItem(m metadata.MovieResult) metadata.DiscoverItem {
	return metadata.DiscoverItem{
		MediaType: "movie", TMDBID: m.TMDBID, Title: m.Title, Year: m.Year, Overview: m.Overview,
		PosterURL: m.PosterURL, VoteAverage: m.VoteAverage, ReleaseDate: m.ReleaseDate,
	}
}

// handleDiscoverCollections is one row per movie collection the library has started and
// not finished: "Complete the Alien Collection", the released films it lacks. At most
// four, each with at least two films to get, the most complete first.
//
//	GET /api/v1/discover/collections → {rows: [{collection_id, title, items}]}
func (a *api) handleDiscoverCollections(w http.ResponseWriter, r *http.Request) {
	getter, ok := a.deps.Discovery.(collectionGetter)
	if a.deps.Movies == nil || !ok || !a.metadataReady() {
		a.writeJSON(w, http.StatusOK, map[string]any{"rows": []collectionRow{}})
		return
	}
	ctx := r.Context()
	var rows []collectionRowItems
	if v, hit := collectionsCache.Load(a); hit && time.Since(v.(*collectionsCacheEntry).at) < collectionsTTL {
		rows = v.(*collectionsCacheEntry).rows
	} else {
		movies, err := a.deps.Movies.List(ctx)
		if err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not list movies")
			return
		}
		rows = buildCollectionRows(ctx, getter, movies)
		collectionsCache.Store(a, &collectionsCacheEntry{at: time.Now(), rows: rows})
	}
	out := make([]collectionRow, 0, len(rows))
	for _, row := range rows {
		out = append(out, collectionRow{CollectionID: row.id, Title: row.title, Items: a.enrichCards(ctx, row.items)})
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"rows": out})
}

// buildCollectionRows picks the rows: the started collections (most films owned first,
// capped so this stays a few requests), each judged on its released members, ordered by
// how complete it is.
func buildCollectionRows(ctx context.Context, getter collectionGetter, library []movies.Movie) []collectionRowItems {
	owned := map[int]bool{}
	ownedPer := map[int]int{}
	names := map[int]string{}
	for _, m := range library {
		owned[m.TMDBID] = true
		if m.Extra != nil && m.Extra.CollectionID > 0 {
			ownedPer[m.Extra.CollectionID]++
			names[m.Extra.CollectionID] = m.Extra.CollectionName
		}
	}
	ids := make([]int, 0, len(ownedPer))
	for id := range ownedPer {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool {
		if ownedPer[ids[i]] != ownedPer[ids[j]] {
			return ownedPer[ids[i]] > ownedPer[ids[j]]
		}
		if names[ids[i]] != names[ids[j]] {
			return names[ids[i]] < names[ids[j]]
		}
		return ids[i] < ids[j]
	})
	if len(ids) > collectionsFetchMax {
		ids = ids[:collectionsFetchMax]
	}
	type candidate struct {
		row      collectionRowItems
		complete float64
		have     int
	}
	today := time.Now().Format("2006-01-02")
	var cands []candidate
	for _, id := range ids {
		c, err := getter.GetCollection(ctx, id)
		if err != nil || c == nil || c.Name == "" {
			continue
		}
		released, have := 0, 0
		var missing []metadata.DiscoverItem
		for _, m := range c.Members {
			if !releasedBy(m, today) {
				continue
			}
			released++
			if owned[m.TMDBID] {
				have++
				continue
			}
			if m.PosterURL != "" {
				missing = append(missing, memberItem(m))
			}
		}
		if len(missing) < collectionMinMissing || released == 0 {
			continue
		}
		cands = append(cands, candidate{
			row:      collectionRowItems{id: c.ID, title: "Complete the " + c.Name, items: missing},
			complete: float64(have) / float64(released), have: have,
		})
	}
	sort.SliceStable(cands, func(i, j int) bool {
		if cands[i].complete != cands[j].complete {
			return cands[i].complete > cands[j].complete
		}
		if cands[i].have != cands[j].have {
			return cands[i].have > cands[j].have
		}
		return cands[i].row.title < cands[j].row.title
	})
	rows := make([]collectionRowItems, 0, collectionsRowsMax)
	for _, cd := range cands {
		if len(rows) >= collectionsRowsMax {
			break
		}
		rows = append(rows, cd.row)
	}
	return rows
}

// collectionResponse is a collection's page.
type collectionResponse struct {
	ID          int            `json:"id"`
	Name        string         `json:"name"`
	Overview    string         `json:"overview,omitempty"`
	PosterURL   string         `json:"poster_url,omitempty"`
	BackdropURL string         `json:"backdrop_url,omitempty"`
	Items       []discoverCard `json:"items"`
	Owned       int            `json:"owned"` // released members in the library
	Total       int            `json:"total"` // released members
}

// handleDiscoverCollection is a collection's page: every member (adult ones never — the
// provider leaves them out), with the viewer's badges, and how much of it is here.
//
//	GET /api/v1/discover/collection/{id}
func (a *api) handleDiscoverCollection(w http.ResponseWriter, r *http.Request) {
	if !a.discoveryReady(w, r) {
		return
	}
	getter, ok := a.deps.Discovery.(collectionGetter)
	if !ok {
		a.writeError(w, http.StatusNotFound, "not available")
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id <= 0 {
		a.writeError(w, http.StatusBadRequest, "invalid collection id")
		return
	}
	ctx := r.Context()
	c, err := getter.GetCollection(ctx, id)
	if errors.Is(err, metadata.ErrNotFound) || (err == nil && (c == nil || len(c.Members) == 0)) {
		a.writeError(w, http.StatusNotFound, "That collection isn't available.")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	snap := a.discoverSnapshot(ctx)
	today := time.Now().Format("2006-01-02")
	resp := collectionResponse{ID: c.ID, Name: c.Name, Overview: c.Overview, PosterURL: c.PosterURL, BackdropURL: c.BackdropURL}
	var items []metadata.DiscoverItem
	for _, m := range c.Members {
		if releasedBy(m, today) {
			resp.Total++
			if snap.movIn[m.TMDBID] {
				resp.Owned++
			}
		}
		if m.PosterURL != "" {
			items = append(items, memberItem(m))
		}
	}
	resp.Items = a.enrichCards(ctx, items)
	a.writeJSON(w, http.StatusOK, resp)
}
