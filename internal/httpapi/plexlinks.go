package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/requests"
)

// PlexLinker finds titles in the owner's Plex server from memory (insights' library
// index) and builds app.plex.tv links to them. *insights.Service is one.
type PlexLinker interface {
	WatchURL(ctx context.Context, media string, ids insights.ExternalIDs) string
}

// plexURL is the Watch on Plex link for a library title, "" when there's none (Plex not
// set up, the title not in Plex yet, or no linker wired). The library record supplies the
// TVDB and IMDb ids that legacy-agent libraries match on.
func (a *api) plexURL(ctx context.Context, media string, tmdbID int) string {
	// The TMDB id alone finds most titles from memory; only a miss reads the library
	// record, so a page of requests doesn't cost a query each.
	if u := a.watchURL(ctx, media, insights.ExternalIDs{TMDB: tmdbID}); u != "" {
		return u
	}
	ids := a.plexIDs(ctx, media, tmdbID)
	if ids.TVDB == 0 && ids.IMDB == "" {
		return ""
	}
	return a.watchURL(ctx, media, ids)
}

// setRequestPlexURLs gives each delivered movie or show request its Watch on Plex link,
// when Plex has the title. Run after Track, which sets the stage.
func (a *api) setRequestPlexURLs(ctx context.Context, list []requests.Request) {
	if a.deps.PlexLinks == nil {
		return
	}
	for i := range list {
		rq := &list[i]
		if rq.Tracking == nil || (rq.Tracking.Stage != requests.StageAvailable && rq.Tracking.Stage != requests.StagePartial) {
			continue
		}
		rq.PlexURL = a.plexURL(ctx, rq.MediaType, rq.TMDBID)
	}
}

// plexIDs is a title's outside ids from its library record (just the TMDB id when it
// isn't in the library, or no linker is wired — then nothing is read).
func (a *api) plexIDs(ctx context.Context, media string, tmdbID int) insights.ExternalIDs {
	ids := insights.ExternalIDs{TMDB: tmdbID}
	if a.deps.PlexLinks == nil || tmdbID <= 0 {
		return ids
	}
	switch media {
	case "movie":
		if a.deps.Movies != nil {
			if m, err := a.deps.Movies.GetByTMDB(ctx, tmdbID); err == nil {
				ids.IMDB = m.IMDBID
			}
		}
	case "series":
		if a.deps.Series != nil {
			if s, err := a.deps.Series.GetByTMDB(ctx, tmdbID); err == nil {
				ids.TVDB, ids.IMDB = s.TVDBID, s.IMDBID
			}
		}
	}
	return ids
}

// watchURL asks the linker ("" without one, or for a media type Plex doesn't hold).
func (a *api) watchURL(ctx context.Context, media string, ids insights.ExternalIDs) string {
	if a.deps.PlexLinks == nil || (media != "movie" && media != "series") {
		return ""
	}
	if ids.TMDB <= 0 && ids.TVDB <= 0 && ids.IMDB == "" {
		return ""
	}
	return a.deps.PlexLinks.WatchURL(ctx, media, ids)
}

// handlePlexLink answers GET /plex/link?media_type=movie|series&tmdb_id=N with {url}, or
// 204 when the title isn't in Plex (or Plex isn't set up). Any signed-in person may ask:
// the link opens Plex's own page, which asks them to sign in to Plex. It never carries
// the token or the server's address.
func (a *api) handlePlexLink(w http.ResponseWriter, r *http.Request) {
	media := r.URL.Query().Get("media_type")
	tmdbID, err := strconv.Atoi(r.URL.Query().Get("tmdb_id"))
	if (media != "movie" && media != "series") || err != nil || tmdbID <= 0 {
		a.writeError(w, http.StatusBadRequest, "media_type (movie or series) and tmdb_id are required")
		return
	}
	u := a.plexURL(r.Context(), media, tmdbID)
	if u == "" {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]string{"url": u})
}
