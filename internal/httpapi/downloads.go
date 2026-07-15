package httpapi

import (
	"context"
	"net/http"

	"github.com/tristenlammi/arrmada/internal/download"
)

var allowedActions = map[string]bool{"recheck": true, "reannounce": true, "prio_up": true, "prio_down": true}

// handlePauseDownload stops an in-progress torrent.
func (a *api) handlePauseDownload(w http.ResponseWriter, r *http.Request) {
	if err := a.deps.Downloads.Pause(r.Context(), r.PathValue("hash")); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "paused"})
}

// handleResumeDownload restarts a stopped torrent.
func (a *api) handleResumeDownload(w http.ResponseWriter, r *http.Request) {
	if err := a.deps.Downloads.Resume(r.Context(), r.PathValue("hash")); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "resumed"})
}

// handleDeleteDownload removes a torrent, optionally with its data (?delete_data=true).
func (a *api) handleDeleteDownload(w http.ResponseWriter, r *http.Request) {
	deleteData := r.URL.Query().Get("delete_data") == "true"
	if err := a.deps.Downloads.Remove(r.Context(), r.PathValue("hash"), deleteData); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTorrentAction runs a per-torrent command: recheck, reannounce, or move
// up/down the queue.
func (a *api) handleTorrentAction(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action string `json:"action"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if !allowedActions[req.Action] {
		a.writeError(w, http.StatusBadRequest, "unknown action")
		return
	}
	if err := a.deps.Downloads.Action(r.Context(), r.PathValue("hash"), req.Action); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handleGetClientSettings returns a client's tunable settings (speed limits, etc.).
func (a *api) handleGetClientSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	s, err := a.deps.Downloads.GetSettings(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, s)
}

// handleSetClientSettings writes a client's tunable settings.
func (a *api) handleSetClientSettings(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var s download.ClientSettings
	if !a.decodeJSON(w, r, &s) {
		return
	}
	if err := a.deps.Downloads.SetSettings(r.Context(), id, s); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "saved"})
}

// handleBlockDownload removes a torrent, blocklists the release for its movie, and
// searches for an alternate — the "grab something else" action.
func (a *api) handleBlockDownload(w http.ResponseWriter, r *http.Request) {
	hash := r.PathValue("hash")
	var req struct {
		Name string `json:"name"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	go a.bg(func(ctx context.Context) error { return a.deps.Automation.BlockRelease(ctx, hash, req.Name) }, "block download", 0)
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "blocking"})
}
