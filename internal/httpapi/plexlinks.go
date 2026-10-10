package httpapi

import (
	"context"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/insights"
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
	if a.deps.PlexLinks == nil || tmdbID <= 0 {
		return ""
	}
	ids := insights.ExternalIDs{TMDB: tmdbID}
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
	default:
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
