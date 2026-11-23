package httpapi

import (
	"errors"
	"net/http"

	"github.com/tristenlammi/arrmada/internal/convert"
)

// handleConvertExclusions — GET /api/v1/convert/exclusions: the films, shows and seasons
// you told Convert to leave alone ("Don't convert this"). The folder and resolution rules
// are Convert settings.
func (a *api) handleConvertExclusions(w http.ResponseWriter, r *http.Request) {
	items, err := a.deps.Convert.TitleExclusions(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the titles Convert leaves alone")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items})
}

// handleConvertExclusionSet — PUT /api/v1/convert/exclusions: turn "Don't convert this" on
// or off for a film {kind:"movie", movie_id}, a show {kind:"series", series_id} or one of its
// seasons (season). Turning it on takes the title's files off the Convert-now queue; a
// conversion already running is left to finish, and the reply names it.
func (a *api) handleConvertExclusionSet(w http.ResponseWriter, r *http.Request) {
	var req convert.ExcludeRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	res, err := a.deps.Convert.SetTitleExclusion(r.Context(), req)
	switch {
	case errors.Is(err, convert.ErrBadExclusion):
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, convert.ErrNoSuchTitle):
		a.writeError(w, http.StatusNotFound, err.Error())
		return
	case err != nil:
		a.writeError(w, http.StatusInternalServerError, "could not save that")
		return
	}
	a.writeJSON(w, http.StatusOK, res)
}
