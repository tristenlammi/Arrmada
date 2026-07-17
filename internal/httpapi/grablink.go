package httpapi

import (
	"encoding/base64"
	"net/http"

	"github.com/tristenlammi/arrmada/internal/torrentmeta"
)

// torrentUpload is the shared request body: a base64-encoded .torrent file (+ optional
// filename and the title to attribute the grab to).
type torrentUpload struct {
	Torrent  string `json:"torrent"` // base64 .torrent file bytes
	Filename string `json:"filename"`
	Title    string `json:"title"`
}

func (a *api) decodeTorrent(w http.ResponseWriter, req torrentUpload) ([]byte, bool) {
	data, err := base64.StdEncoding.DecodeString(req.Torrent)
	if err != nil || len(data) == 0 {
		a.writeError(w, http.StatusBadRequest, "no valid .torrent file was uploaded")
		return nil, false
	}
	return data, true
}

// handleGrabPreview reads an uploaded .torrent's name + size + files, so the UI can
// confirm it's the right release before grabbing.
//
//	POST /api/v1/grab/preview  {torrent}
func (a *api) handleGrabPreview(w http.ResponseWriter, r *http.Request) {
	var req torrentUpload
	if !a.decodeJSON(w, r, &req) {
		return
	}
	data, ok := a.decodeTorrent(w, req)
	if !ok {
		return
	}
	info, err := torrentmeta.ParseTorrent(data)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, info)
}

// handleMovieGrabTorrent adds an uploaded .torrent to the download client, attributed
// to a movie.
func (a *api) handleMovieGrabTorrent(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req torrentUpload
	if !a.decodeJSON(w, r, &req) {
		return
	}
	data, ok := a.decodeTorrent(w, req)
	if !ok {
		return
	}
	if err := a.deps.Automation.GrabMovieTorrent(r.Context(), id, data, req.Filename, req.Title); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "grabbed"})
}

// handleSeriesGrabTorrent adds an uploaded .torrent to the download client, attributed
// to a series.
func (a *api) handleSeriesGrabTorrent(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req torrentUpload
	if !a.decodeJSON(w, r, &req) {
		return
	}
	data, ok := a.decodeTorrent(w, req)
	if !ok {
		return
	}
	if err := a.deps.Automation.GrabSeriesTorrent(r.Context(), id, data, req.Filename, req.Title); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "grabbed"})
}
