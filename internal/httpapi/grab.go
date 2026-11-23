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
	// VersionID is the track the grab is for (0 = the default track). A token from a
	// track's own search already names its track; a different one here is refused.
	VersionID int64 `json:"version_id,omitempty"`
	// ReplaceSmaller marks a grab from "Find a smaller release": the download is checked
	// against the file it replaces before it's imported, and leaves that file's release as
	// the baseline (automation/smaller.go).
	ReplaceSmaller bool `json:"replace_smaller,omitempty"`
}

// handleGrab closes the acquisition loop: fetch a release's download link
// (auth-gated .torrent, scraped magnet, or a plain URL) and hand it to a
// download client. Shares the Grab logic with automatic searching.
//
//	POST /api/v1/grab  {token, movie_id, version_id?}
func (a *api) handleGrab(w http.ResponseWriter, r *http.Request) {
	var req grabRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	ref, ok := a.resolveRelease(w, r, req.Token, automation.ReleaseKindMovie, req.MovieID)
	if !ok {
		return
	}
	// The track: the one the search ranked for, which the token carries. A body naming a
	// different track would grab a release judged against another track's profile and
	// edition, so it's refused rather than quietly re-filed.
	versionID := ref.VersionID
	if req.VersionID != 0 && req.VersionID != ref.VersionID {
		a.writeError(w, http.StatusBadRequest, "that result was searched for a different version — search again from the version")
		return
	}
	if !a.movieTrackOK(w, r, req.MovieID, versionID) {
		return
	}

	hash, err := a.deps.Automation.GrabForMovie(r.Context(), ref.MediaID, ref.Indexer, ref.DownloadURL, ref.Title)
	if err != nil {
		a.deps.Log.Warn("grab failed", "indexer", ref.Indexer, "title", ref.Title, "err", err)
		a.writeGrabError(w, err, ref)
		return
	}
	// Track it so seed cleanup / stall detection manage it like an auto grab. The info
	// hash goes on the row so the torrent can be matched back regardless of how the
	// client ends up naming it.
	a.deps.Automation.RecordManualGrab(r.Context(), ref.MediaID, versionID, ref.Title, ref.Indexer, hash)
	if req.ReplaceSmaller {
		if err := a.deps.Automation.MarkReplaceGrab(r.Context(), ref.MediaID, versionID, ref.Title); err != nil {
			// The download goes ahead as an ordinary grab; only its check and baseline are lost.
			a.deps.Log.Warn("smaller release: couldn't record what the grab replaces", "title", ref.Title, "err", err)
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "grabbed", "title": ref.Title})
}

// movieTrackOK checks that vid is one of movie id's tracks (0, the default track, always
// is), answering 404 when it isn't.
func (a *api) movieTrackOK(w http.ResponseWriter, r *http.Request, id, vid int64) bool {
	if vid == 0 {
		return true
	}
	versions, err := a.deps.Movies.VersionRows(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusNotFound, "movie not found")
		return false
	}
	for _, v := range versions {
		if v.ID == vid {
			return true
		}
	}
	a.writeError(w, http.StatusNotFound, "that version isn't one of this movie's")
	return false
}
