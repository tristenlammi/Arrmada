package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/parser"
)

// discoverCard is a DiscoverItem enriched with the viewer's library/request status so
// the UI can show the right badge (Available / Requested / requestable) and a
// download-progress bar for items currently downloading.
type discoverCard struct {
	metadata.DiscoverItem
	InLibrary        bool    `json:"in_library"`
	HasFile          bool    `json:"has_file"`
	RequestStatus    string  `json:"request_status,omitempty"`    // pending | approved | declined
	DownloadProgress float64 `json:"download_progress,omitempty"` // 0..1 while downloading
}

// discoverEnrichSnap holds the precomputed lookup maps enrichDiscover needs: library
// membership, file presence, in-flight download progress, and request status.
type discoverEnrichSnap struct {
	movIn, movHave map[int]bool
	serIn, serHave map[int]bool
	prog           map[string]float64 // "movie:123" / "series:123" -> progress 0..1
	reqStatus      map[string]string  // "movie:123" / "series:123" -> status
}

// enrichSnapTTL: a Discover page render fires ~6 row requests at once and the frontend
// polls every 8s; snapshotting the enrichment inputs for 15s means each burst re-lists
// the library/requests/queue once instead of per-row.
const enrichSnapTTL = 15 * time.Second

// enrichSnaps keys the snapshot per api instance (the api struct lives in server.go and
// can't grow a field here without crossing file ownership; a process runs one server, so
// this map holds a single entry in practice).
var enrichSnaps sync.Map // *api -> *enrichSnapEntry

type enrichSnapEntry struct {
	mu   sync.Mutex
	at   time.Time
	snap *discoverEnrichSnap
}

// discoverSnapshot returns the (possibly cached) enrichment maps. Snapshots built while
// any backing store errored are served but not cached, so a transient failure clears on
// the next request rather than sticking for the TTL.
func (a *api) discoverSnapshot(ctx context.Context) *discoverEnrichSnap {
	v, _ := enrichSnaps.LoadOrStore(a, &enrichSnapEntry{})
	e := v.(*enrichSnapEntry)
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.snap != nil && time.Since(e.at) < enrichSnapTTL {
		return e.snap
	}
	snap, complete := a.buildDiscoverSnapshot(ctx)
	if complete {
		e.snap, e.at = snap, time.Now()
	}
	return snap
}

// buildDiscoverSnapshot lists the library, requests, and download queue once and folds
// them into lookup maps. complete is false when any source errored (partial data).
func (a *api) buildDiscoverSnapshot(ctx context.Context) (snap *discoverEnrichSnap, complete bool) {
	snap = &discoverEnrichSnap{
		movIn: map[int]bool{}, movHave: map[int]bool{},
		serIn: map[int]bool{}, serHave: map[int]bool{},
		prog: map[string]float64{}, reqStatus: map[string]string{},
	}
	complete = true
	queue, err := a.deps.Downloads.Queue(ctx)
	if err != nil {
		complete = false
	}
	if ms, err := a.deps.Movies.List(ctx); err == nil {
		for _, m := range ms {
			snap.movIn[m.TMDBID] = true
			snap.movHave[m.TMDBID] = m.HasFile
			if len(queue) > 0 {
				if d := downloadFor(queue, m); d != nil {
					snap.prog["movie:"+strconv.Itoa(m.TMDBID)] = d.Progress
				}
			}
		}
	} else {
		complete = false
	}
	if ss, err := a.deps.Series.List(ctx); err == nil {
		for _, s := range ss {
			snap.serIn[s.TMDBID] = true
			snap.serHave[s.TMDBID] = s.Stats != nil && s.Stats.HaveFiles > 0
			if len(queue) > 0 {
				if p, ok := seriesQueueProgress(queue, s.Title); ok {
					snap.prog["series:"+strconv.Itoa(s.TMDBID)] = p
				}
			}
		}
	} else {
		complete = false
	}
	if rs, err := a.deps.Requests.List(ctx, "", 0); err == nil {
		for _, req := range rs {
			snap.reqStatus[req.MediaType+":"+strconv.Itoa(req.TMDBID)] = req.Status
		}
	} else {
		complete = false
	}
	return snap, complete
}

// enrichDiscover attaches library + request status (and live download progress) to
// a batch of discover items.
func (a *api) enrichDiscover(w http.ResponseWriter, r *http.Request, items []metadata.DiscoverItem) {
	a.writeJSON(w, http.StatusOK, map[string]any{"items": a.enrichCards(r.Context(), items)})
}

// enrichCards annotates raw discover items with the viewer's library/request/download
// status, sharing the 15s enrichment snapshot. Split out of enrichDiscover so the
// personalized row can enrich and then filter before writing.
func (a *api) enrichCards(ctx context.Context, items []metadata.DiscoverItem) []discoverCard {
	snap := a.discoverSnapshot(ctx)
	cards := make([]discoverCard, 0, len(items))
	for _, it := range items {
		c := discoverCard{DiscoverItem: it}
		if it.MediaType == "movie" {
			c.InLibrary, c.HasFile = snap.movIn[it.TMDBID], snap.movHave[it.TMDBID]
		} else {
			c.InLibrary, c.HasFile = snap.serIn[it.TMDBID], snap.serHave[it.TMDBID]
		}
		c.RequestStatus = snap.reqStatus[it.MediaType+":"+strconv.Itoa(it.TMDBID)]
		c.DownloadProgress = snap.prog[it.MediaType+":"+strconv.Itoa(it.TMDBID)]
		cards = append(cards, c)
	}
	return cards
}

// seriesQueueProgress returns the progress of an in-flight download whose parsed
// series title matches, for the discover progress bar.
func seriesQueueProgress(queue []download.Item, title string) (float64, bool) {
	return queueProgressByTitle(queue, title, 0)
}

// queueProgressByTitle returns the progress (0..1) of the first not-yet-finished
// download whose parsed title matches (and year, when both are known).
func queueProgressByTitle(queue []download.Item, title string, year int) (float64, bool) {
	want := normKey(title)
	for i := range queue {
		it := queue[i]
		if it.Progress >= 1 {
			continue
		}
		p := parser.Parse(it.Name)
		if normKey(p.Title) != want {
			continue
		}
		if p.Year != 0 && year != 0 && absInt(p.Year-year) > 1 {
			continue
		}
		return it.Progress, true
	}
	return 0, false
}

func (a *api) discoveryReady(w http.ResponseWriter) bool {
	if a.deps.Discovery == nil || !a.deps.Discovery.Available() {
		a.writeError(w, http.StatusBadRequest, "metadata isn't configured — set ARRMADA_TMDB_API_KEY")
		return false
	}
	return true
}

func (a *api) handleDiscoverTrending(w http.ResponseWriter, r *http.Request) {
	if !a.discoveryReady(w) {
		return
	}
	media := r.URL.Query().Get("media") // all | movie | series
	items, err := a.deps.Discovery.Trending(r.Context(), media)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.enrichDiscover(w, r, items)
}

func (a *api) handleDiscoverPopular(w http.ResponseWriter, r *http.Request) {
	if !a.discoveryReady(w) {
		return
	}
	items, err := a.deps.Discovery.Popular(r.Context(), r.URL.Query().Get("media"))
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.enrichDiscover(w, r, items)
}

func (a *api) handleDiscoverSearch(w http.ResponseWriter, r *http.Request) {
	if !a.discoveryReady(w) {
		return
	}
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	if q == "" {
		a.enrichDiscover(w, r, nil)
		return
	}
	items, err := a.deps.Discovery.Search(r.Context(), q)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.enrichDiscover(w, r, items)
}

func (a *api) handleDiscoverUpcoming(w http.ResponseWriter, r *http.Request) {
	if !a.discoveryReady(w) {
		return
	}
	items, err := a.deps.Discovery.Upcoming(r.Context(), r.URL.Query().Get("media"))
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.enrichDiscover(w, r, items)
}

func (a *api) handleDiscoverByGenre(w http.ResponseWriter, r *http.Request) {
	if !a.discoveryReady(w) {
		return
	}
	genre, _ := strconv.Atoi(r.URL.Query().Get("genre"))
	if genre == 0 {
		a.writeError(w, http.StatusBadRequest, "genre id is required")
		return
	}
	items, err := a.deps.Discovery.DiscoverByGenre(r.Context(), r.URL.Query().Get("media"), genre)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.enrichDiscover(w, r, items)
}

// handleMediaDetail returns the full record behind the discover detail modal: TMDB
// metadata + cast/crew, plus external ratings (IMDB/RT/Metacritic) when OMDb is set.
func (a *api) handleMediaDetail(w http.ResponseWriter, r *http.Request) {
	if !a.discoveryReady(w) {
		return
	}
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil || id == 0 {
		a.writeError(w, http.StatusBadRequest, "invalid tmdb id")
		return
	}
	d, err := a.deps.Discovery.MediaDetails(r.Context(), r.PathValue("media"), id)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if a.deps.Ratings != nil && a.deps.Ratings.Available() && d.IMDBID != "" {
		if rt, err := a.deps.Ratings.Ratings(r.Context(), d.IMDBID); err == nil {
			d.Ratings.IMDB = rt.IMDB
			d.Ratings.RottenTomatoes = rt.RottenTomatoes
			d.Ratings.Metacritic = rt.Metacritic
		}
	}
	a.writeJSON(w, http.StatusOK, d)
}

func (a *api) handleDiscoverGenres(w http.ResponseWriter, r *http.Request) {
	if !a.discoveryReady(w) {
		return
	}
	genres, err := a.deps.Discovery.Genres(r.Context(), r.URL.Query().Get("media"))
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if genres == nil {
		genres = []metadata.Genre{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"genres": genres})
}
