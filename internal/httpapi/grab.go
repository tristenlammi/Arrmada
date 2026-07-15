package httpapi

import (
	"net/http"
)

type grabRequest struct {
	Indexer     string `json:"indexer"`
	DownloadURL string `json:"download_url"`
	Title       string `json:"title"`
	MovieID     int64  `json:"movie_id"` // so a manual grab is seed-managed like an auto one
}

// handleGrab closes the acquisition loop: fetch a release's download link
// (auth-gated .torrent, scraped magnet, or a plain URL) and hand it to a
// download client. Shares the Grab logic with automatic searching.
//
//	POST /api/v1/grab  {indexer, download_url, title}
func (a *api) handleGrab(w http.ResponseWriter, r *http.Request) {
	var req grabRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.DownloadURL == "" {
		a.writeError(w, http.StatusBadRequest, "download_url is required")
		return
	}

	if err := a.deps.Automation.Grab(r.Context(), req.Indexer, req.DownloadURL, req.Title); err != nil {
		a.deps.Log.Warn("grab failed", "indexer", req.Indexer, "title", req.Title, "err", err)
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// Track it so seed cleanup / stall detection manage it like an auto grab.
	a.deps.Automation.RecordManualGrab(r.Context(), req.MovieID, req.Title, req.Indexer)
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "grabbed", "title": req.Title})
}
