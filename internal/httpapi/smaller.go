package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// handleMovieSmallerReleases is "Find a smaller release": the interactive search for one of
// the movie's files, narrowed to releases that would be a sensible smaller replacement for
// it, each saying why it qualifies and what it would give up. Grabbing one is POST /grab
// with replace_smaller set.
//
//	GET /api/v1/movies/{id}/smaller-releases?version=VID   (VID 0 or absent = the default file)
func (a *api) handleMovieSmallerReleases(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var versionID int64
	if v := r.URL.Query().Get("version"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			a.writeError(w, http.StatusBadRequest, "version must be a version id")
			return
		}
		versionID = n
	}
	ctx, cancel := context.WithTimeout(indexer.WithInteractive(r.Context()), 90*time.Second)
	defer cancel()
	list, err := a.deps.Automation.SmallerReleases(ctx, id, versionID)
	switch {
	case errors.Is(err, movies.ErrNotFound):
		a.writeError(w, http.StatusNotFound, "movie not found")
		return
	case errors.Is(err, automation.ErrNoFileToReplace):
		a.writeError(w, http.StatusConflict, err.Error())
		return
	case err != nil:
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	scope := automation.ReleaseRef{MediaKind: automation.ReleaseKindMovie, MediaID: id, VersionID: versionID}
	a.tokenize(r, &list.ReleaseList, scope)
	a.writeJSON(w, http.StatusOK, list)
}
