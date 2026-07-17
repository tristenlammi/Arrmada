package httpapi

import (
	"net/http"
	"time"

	"github.com/tristenlammi/arrmada/internal/torrentmeta"
)

var linkPreviewClient = &http.Client{Timeout: 25 * time.Second}

// handleGrabPreview resolves a pasted magnet/.torrent link to its name + size + files,
// so the UI can confirm it's the right thing before grabbing.
//
//	POST /api/v1/grab/preview  {link}
func (a *api) handleGrabPreview(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Link string `json:"link"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	info, err := torrentmeta.Preview(r.Context(), linkPreviewClient, req.Link)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, info)
}

// handleMovieGrabLink hands a pasted link to the download client, attributed to a movie.
func (a *api) handleMovieGrabLink(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Link  string `json:"link"`
		Title string `json:"title"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Automation.GrabMovieLink(r.Context(), id, req.Link, req.Title); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "grabbed"})
}

// handleSeriesGrabLink hands a pasted link to the download client, attributed to a series.
func (a *api) handleSeriesGrabLink(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Link  string `json:"link"`
		Title string `json:"title"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Automation.GrabSeriesLink(r.Context(), id, req.Link, req.Title); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "grabbed"})
}
