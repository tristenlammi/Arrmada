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
	// PlexIndexReady is false while no lookup can succeed (Plex not set up, the index
	// not built yet), so callers skip reading library records for fallback ids.
	PlexIndexReady(ctx context.Context) bool
}

func (a *api) plexReady(ctx context.Context) bool {
	return a.deps.PlexLinks != nil && a.deps.PlexLinks.PlexIndexReady(ctx)
}

// plexURL is the Watch on Plex link for a title, "" when there's none (Plex not set up,
// the title not in Plex yet, or no linker wired). The TMDB id alone finds most titles;
// on a miss the library record supplies the TVDB and IMDb ids legacy-agent libraries
// match on.
func (a *api) plexURL(ctx context.Context, media string, tmdbID int) string {
	return a.plexURLWith(ctx, media, insights.ExternalIDs{TMDB: tmdbID})
}

// plexURLWith is plexURL starting from ids already known (a detail's IMDb id).
func (a *api) plexURLWith(ctx context.Context, media string, ids insights.ExternalIDs) string {
	if !a.plexReady(ctx) {
		return ""
	}
	if u := a.watchURL(ctx, media, ids); u != "" {
		return u
	}
	more := a.plexIDs(ctx, media, ids.TMDB)
	if (more.TVDB == 0 || more.TVDB == ids.TVDB) && (more.IMDB == "" || more.IMDB == ids.IMDB) {
		return "" // the library knows nothing the first try didn't
	}
	if more.IMDB == "" {
		more.IMDB = ids.IMDB
	}
	return a.watchURL(ctx, media, more)
}

// setRequestPlexURLs gives each delivered movie or show request its Watch on Plex link,
// when Plex has the title. Run after Track, which sets the stage. Titles the TMDB id
// doesn't find are looked up again with their library ids, read in one query per kind.
func (a *api) setRequestPlexURLs(ctx context.Context, list []requests.Request) {
	if !a.plexReady(ctx) {
		return
	}
	var missed []int
	missMovies, missSeries := []int{}, []int{}
	for i := range list {
		rq := &list[i]
		if rq.Tracking == nil || (rq.Tracking.Stage != requests.StageAvailable && rq.Tracking.Stage != requests.StagePartial) {
			continue
		}
		if rq.PlexURL = a.watchURL(ctx, rq.MediaType, insights.ExternalIDs{TMDB: rq.TMDBID}); rq.PlexURL != "" {
			continue
		}
		switch rq.MediaType {
		case "movie":
			missMovies = append(missMovies, rq.TMDBID)
		case "series":
			missSeries = append(missSeries, rq.TMDBID)
		default:
			continue
		}
		missed = append(missed, i)
	}
	if len(missed) == 0 {
		return
	}
	ids := map[string]insights.ExternalIDs{}
	if len(missMovies) > 0 && a.deps.Movies != nil {
		if ms, err := a.deps.Movies.ByTMDBIDs(ctx, missMovies); err == nil {
			for _, m := range ms {
				ids["movie:"+strconv.Itoa(m.TMDBID)] = insights.ExternalIDs{TMDB: m.TMDBID, IMDB: m.IMDBID}
			}
		}
	}
	if len(missSeries) > 0 && a.deps.Series != nil {
		if ss, err := a.deps.Series.ByTMDBIDs(ctx, missSeries); err == nil {
			for _, s := range ss {
				ids["series:"+strconv.Itoa(s.TMDBID)] = insights.ExternalIDs{TMDB: s.TMDBID, TVDB: s.TVDBID, IMDB: s.IMDBID}
			}
		}
	}
	for _, i := range missed {
		rq := &list[i]
		if x, ok := ids[rq.MediaType+":"+strconv.Itoa(rq.TMDBID)]; ok && (x.TVDB > 0 || x.IMDB != "") {
			rq.PlexURL = a.watchURL(ctx, rq.MediaType, x)
		}
	}
}

// plexIDs is a title's outside ids from its library record (just the TMDB id when it
// isn't in the library).
func (a *api) plexIDs(ctx context.Context, media string, tmdbID int) insights.ExternalIDs {
	ids := insights.ExternalIDs{TMDB: tmdbID}
	if tmdbID <= 0 {
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
