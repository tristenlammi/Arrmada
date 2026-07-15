package httpapi

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/metadata"
)

// discoverCard is a DiscoverItem enriched with the viewer's library/request status so
// the UI can show the right badge (Available / Requested / requestable).
type discoverCard struct {
	metadata.DiscoverItem
	InLibrary     bool   `json:"in_library"`
	HasFile       bool   `json:"has_file"`
	RequestStatus string `json:"request_status,omitempty"` // pending | approved | declined
}

// enrichDiscover attaches library + request status to a batch of discover items.
func (a *api) enrichDiscover(w http.ResponseWriter, r *http.Request, items []metadata.DiscoverItem) {
	ctx := r.Context()
	movIn, movHave := map[int]bool{}, map[int]bool{}
	if ms, err := a.deps.Movies.List(ctx); err == nil {
		for _, m := range ms {
			movIn[m.TMDBID] = true
			movHave[m.TMDBID] = m.HasFile
		}
	}
	serIn, serHave := map[int]bool{}, map[int]bool{}
	if ss, err := a.deps.Series.List(ctx); err == nil {
		for _, s := range ss {
			serIn[s.TMDBID] = true
			serHave[s.TMDBID] = s.Stats != nil && s.Stats.HaveFiles > 0
		}
	}
	reqStatus := map[string]string{} // "movie:123" / "series:123" -> status
	if rs, err := a.deps.Requests.List(ctx, "", 0); err == nil {
		for _, req := range rs {
			reqStatus[req.MediaType+":"+strconv.Itoa(req.TMDBID)] = req.Status
		}
	}

	cards := make([]discoverCard, 0, len(items))
	for _, it := range items {
		c := discoverCard{DiscoverItem: it}
		if it.MediaType == "movie" {
			c.InLibrary, c.HasFile = movIn[it.TMDBID], movHave[it.TMDBID]
		} else {
			c.InLibrary, c.HasFile = serIn[it.TMDBID], serHave[it.TMDBID]
		}
		c.RequestStatus = reqStatus[it.MediaType+":"+strconv.Itoa(it.TMDBID)]
		cards = append(cards, c)
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": cards})
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
	items, err := a.deps.Discovery.Upcoming(r.Context())
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
