package httpapi

import (
	"net/http"

	"github.com/tristenlammi/arrmada/internal/automation"
)

type grabRequest struct {
	// Token is a release token from this movie's interactive search. The download link
	// itself never reaches the browser, and no link the browser posts is ever fetched.
	Token   string `json:"token"`
	MovieID int64  `json:"movie_id"` // the movie the search was for; the token must match it
}

// handleGrab closes the acquisition loop: fetch a release's download link
// (auth-gated .torrent, scraped magnet, or a plain URL) and hand it to a
// download client. Shares the Grab logic with automatic searching.
//
//	POST /api/v1/grab  {token, movie_id}
func (a *api) handleGrab(w http.ResponseWriter, r *http.Request) {
	var req grabRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	ref, ok := a.resolveRelease(w, r, req.Token, automation.ReleaseKindMovie, req.MovieID)
	if !ok {
		return
	}

	hash, err := a.deps.Automation.Grab(r.Context(), ref.Indexer, ref.DownloadURL, ref.Title)
	if err != nil {
		a.deps.Log.Warn("grab failed", "indexer", ref.Indexer, "title", ref.Title, "err", err)
		a.writeGrabError(w, err, ref)
		return
	}
	// Track it so seed cleanup / stall detection manage it like an auto grab. The info
	// hash goes on the row so the torrent can be matched back regardless of how the
	// client ends up naming it.
	a.deps.Automation.RecordManualGrab(r.Context(), ref.MediaID, ref.Title, ref.Indexer, hash)
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "grabbed", "title": ref.Title})
}
