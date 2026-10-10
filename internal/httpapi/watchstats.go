package httpapi

import (
	"context"
	"errors"
	"net/http"

	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/series"
)

// "Watched by" on the Movie and Series pages: who has played the title on Plex. Staff
// only — a requester never sees anyone else's viewing.

// writeWatchStats answers with the title's stats, or "not available" when Insights isn't
// wired (the page then shows nothing).
func (a *api) writeWatchStats(w http.ResponseWriter, ctx context.Context, media string, ids insights.ExternalIDs, title string, year int) {
	if a.deps.Insights == nil {
		a.writeJSON(w, http.StatusOK, insights.WatchStats{Users: []insights.UserPlays{}})
		return
	}
	ws, err := a.deps.Insights.WatchStats(ctx, media, ids, title, year)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the play history")
		return
	}
	a.writeJSON(w, http.StatusOK, ws)
}

// handleMovieWatchStats answers GET /movies/{id}/watch-stats.
func (a *api) handleMovieWatchStats(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	m, err := a.deps.Movies.Get(r.Context(), id)
	if errors.Is(err, movies.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "movie not found")
		return
	} else if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the movie")
		return
	}
	a.writeWatchStats(w, r.Context(), "movie", insights.ExternalIDs{TMDB: m.TMDBID, IMDB: m.IMDBID}, m.Title, m.Year)
}

// handleSeriesWatchStats answers GET /series/{id}/watch-stats.
func (a *api) handleSeriesWatchStats(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	s, err := a.deps.Series.GetSummary(r.Context(), id)
	if errors.Is(err, series.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "series not found")
		return
	} else if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the series")
		return
	}
	a.writeWatchStats(w, r.Context(), "series", insights.ExternalIDs{TMDB: s.TMDBID, TVDB: s.TVDBID, IMDB: s.IMDBID}, s.Title, s.Year)
}
