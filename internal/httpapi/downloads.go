package httpapi

import (
	"context"
	"net/http"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/diskspace"
	"github.com/tristenlammi/arrmada/internal/download"
)

var allowedActions = map[string]bool{"recheck": true, "reannounce": true, "prio_up": true, "prio_down": true}

// torrentHash reads the {hash} path value and refuses anything that isn't exactly one
// torrent's info hash. qBittorrent reads "all" as every torrent it holds and "a|b" as
// several, so passing the value through let one request act on the whole client.
func (a *api) torrentHash(w http.ResponseWriter, r *http.Request) (string, bool) {
	h := r.PathValue("hash")
	if !download.ValidHash(h) {
		a.writeError(w, http.StatusBadRequest, "not a torrent hash — remove torrents one at a time")
		return "", false
	}
	return h, true
}

// handlePauseDownload stops an in-progress torrent.
func (a *api) handlePauseDownload(w http.ResponseWriter, r *http.Request) {
	hash, ok := a.torrentHash(w, r)
	if !ok {
		return
	}
	if err := a.deps.Downloads.Pause(r.Context(), hash); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "paused"})
}

// handleResumeDownload restarts a stopped torrent.
func (a *api) handleResumeDownload(w http.ResponseWriter, r *http.Request) {
	hash, ok := a.torrentHash(w, r)
	if !ok {
		return
	}
	if err := a.deps.Downloads.Resume(r.Context(), hash); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "resumed"})
}

// handleDeleteDownload removes one torrent the way the user chose:
// ?mode=keep_files (default) | delete_files | block, plus &unmonitor=true to stop wanting
// what it was for and &name=<torrent name> to find its grab when it has no recorded hash.
// The old ?delete_data=true still means delete_files for one release.
func (a *api) handleDeleteDownload(w http.ResponseWriter, r *http.Request) {
	hash, ok := a.torrentHash(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	mode := automation.RemoveMode(q.Get("mode"))
	if mode == "" {
		mode = automation.RemoveKeepFiles
		// The delete_data alias stays for one release so an old cached UI still works.
		if q.Get("delete_data") == "true" {
			mode = automation.RemoveDeleteFiles
		}
	}
	if !automation.ValidRemoveMode(mode) {
		a.writeError(w, http.StatusBadRequest, "mode must be keep_files, delete_files or block")
		return
	}
	unmonitor := q.Get("unmonitor") == "true"
	name := q.Get("name")
	if mode == automation.RemoveBlock {
		if unmonitor {
			a.writeError(w, http.StatusBadRequest, "blocking finds another copy, so it can't also stop wanting the title")
			return
		}
		// Blocking searches for an alternate, which can take a while: run it like the
		// Block button does and answer straight away.
		go a.bg(func(ctx context.Context) error {
			_, err := a.deps.Automation.RemoveDownload(ctx, hash, name, mode, false)
			return err
		}, "block download", 0)
		a.writeJSON(w, http.StatusAccepted, automation.RemoveResult{Mode: mode})
		return
	}
	res, err := a.deps.Automation.RemoveDownload(r.Context(), hash, name, mode, unmonitor)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, res)
}

// handleTorrentAction runs a per-torrent command: recheck, reannounce, or move
// up/down the queue.
func (a *api) handleTorrentAction(w http.ResponseWriter, r *http.Request) {
	hash, ok := a.torrentHash(w, r)
	if !ok {
		return
	}
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
	if err := a.deps.Downloads.Action(r.Context(), hash, req.Action); err != nil {
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
	hash, ok := a.torrentHash(w, r)
	if !ok {
		return
	}
	var req struct {
		Name string `json:"name"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	go a.bg(func(ctx context.Context) error { return a.deps.Automation.BlockRelease(ctx, hash, req.Name) }, "block download", 0)
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "blocking"})
}

// diskGuardStatus is the guard's live view, plus the two facts that decide whether
// the setting can do anything at all: which volume it is actually watching, and
// whether that volume is genuinely separate from the library.
type diskGuardStatus struct {
	download.GuardStatus
	// SharedWithLibrary means the downloads folder and the library measure as the
	// same filesystem. The guard still functions, but it is then watching the whole
	// array rather than a torrent drive, so a threshold tuned for a cache pool is
	// measuring the wrong thing entirely.
	SharedWithLibrary bool   `json:"shared_with_library"`
	LibraryPath       string `json:"library_path"`
}

// handleDiskGuardStatus reports what the disk guard is watching and what it sees.
// The setting depends entirely on ARRMADA_DOWNLOADS_DIR pointing at the torrent
// drive, and there is no way to know that from inside the app — so show the resolved
// path and the reading taken from it, and let the user confirm it themselves.
func (a *api) handleDiskGuardStatus(w http.ResponseWriter, r *http.Request) {
	if a.deps.DiskGuard == nil {
		a.writeError(w, http.StatusServiceUnavailable, "the disk guard isn't running")
		return
	}
	ctx := r.Context()
	out := diskGuardStatus{
		GuardStatus: a.deps.DiskGuard.Status(ctx),
		LibraryPath: a.deps.Config.LibraryDir,
	}
	// The same identity test the dashboard uses: two paths on one filesystem report
	// byte-identical totals, which is cheaper and more portable than a device id.
	if dl, ok := diskspace.Of(a.deps.Config.DownloadsDir); ok {
		if lib, ok := diskspace.Of(a.deps.Config.LibraryDir); ok {
			out.SharedWithLibrary = dl.TotalBytes == lib.TotalBytes && dl.FreeBytes == lib.FreeBytes
		}
	}
	a.writeJSON(w, http.StatusOK, out)
}
