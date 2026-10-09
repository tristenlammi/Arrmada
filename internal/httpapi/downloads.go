package httpapi

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/health"
	"github.com/tristenlammi/arrmada/internal/jobs"
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

// pausableHash is torrentHash for pause and resume, which also take the literal "all":
// the Downloads page's Pause all / Resume all use it, and pausing everything is harmless
// and undone with one click. Delete, action and block stay one hash only.
func (a *api) pausableHash(w http.ResponseWriter, r *http.Request) (string, bool) {
	if r.PathValue("hash") == "all" {
		return "all", true
	}
	return a.torrentHash(w, r)
}

// handlePauseDownload stops an in-progress torrent.
func (a *api) handlePauseDownload(w http.ResponseWriter, r *http.Request) {
	hash, ok := a.pausableHash(w, r)
	if !ok {
		return
	}
	if err := a.deps.Downloads.Pause(r.Context(), hash); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "paused"})
}

// handleResumeDownload restarts a stopped torrent. "all" resumes every paused torrent
// except those the disk guard is holding.
//
// Resume used to go straight to qBittorrent, hash "all" included. The guard skipped
// anything it already held, so a torrent it paused and the user resumed was never
// paused again and the cache pool filled with the guard switched on.
func (a *api) handleResumeDownload(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if r.PathValue("hash") == "all" {
		a.resumeAllDownloads(w, r)
		return
	}
	hash, ok := a.torrentHash(w, r)
	if !ok {
		return
	}
	if holding, st := a.guardHolding(ctx); holding[strings.ToLower(hash)] {
		a.writeError(w, http.StatusConflict, guardHeldMessage(st))
		return
	}
	if err := a.deps.Downloads.Resume(ctx, hash); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "resumed"})
}

// resumeAllDownloads resumes each paused torrent the disk guard isn't holding, and says
// how many it left for the guard so the page can explain why they're still paused.
func (a *api) resumeAllDownloads(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	items, err := a.deps.Downloads.Queue(ctx)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	holding, _ := a.guardHolding(ctx)
	var resume []string
	held := 0
	for _, it := range items {
		if it.State != "paused" {
			continue
		}
		if holding[strings.ToLower(it.Hash)] {
			held++
			continue
		}
		resume = append(resume, it.Hash)
	}
	// One request for the lot: a queue of a few hundred paused torrents would otherwise
	// be a few hundred round trips inside this request.
	if err := a.deps.Downloads.ResumeMany(ctx, resume); err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"status": "resumed", "resumed": len(resume), "held_by_guard": held,
	})
}

// guardHolding returns the torrents the disk guard is holding right now (lowercased
// hashes), with its status for messages. Empty when there's no guard or it isn't
// engaged: a guard that has drained below its resume point, been turned off, or can't
// measure the disk releases what it holds on its next pass, so nothing should be
// refused in its name.
func (a *api) guardHolding(ctx context.Context) (map[string]bool, download.GuardStatus) {
	g := a.deps.DiskGuard
	if g == nil || !g.Engaged(ctx) {
		return nil, download.GuardStatus{}
	}
	return g.Held(ctx), g.Status(ctx)
}

// guardHeldMessage explains a refused resume, including how to override it.
func guardHeldMessage(st download.GuardStatus) string {
	return fmt.Sprintf("Held by the disk guard: %s is %.0f%% full (pauses at %d%%, resumes below %d%%). "+
		"It will resume on its own once space is freed, or turn the guard off in Settings → Downloads.",
		st.Path, st.UsedPct, st.PausePct, st.ResumePct)
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
		if _, _, err := a.submit(r, jobs.Spec{Kind: "download.block", Target: "hash:" + hash, Class: jobs.ClassIndexerSearch, Timeout: 3 * time.Minute,
			Fn: errFn(func(ctx context.Context) error {
				_, err := a.deps.Automation.RemoveDownload(ctx, hash, name, mode, false)
				return err
			})}); err != nil {
			a.writeError(w, http.StatusServiceUnavailable, "couldn't start that just now — try again in a moment")
			return
		}
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
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "download.block", Target: "hash:" + hash, Class: jobs.ClassIndexerSearch, Timeout: 3 * time.Minute,
		Fn: errFn(func(ctx context.Context) error { return a.deps.Automation.BlockRelease(ctx, hash, req.Name) })})
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "blocking"})
}

// diskGuardStatus is the guard's live view, plus the two facts that decide whether
// the setting can do anything at all: which volume it is actually watching, and
// whether that volume is genuinely separate from the library.
type diskGuardStatus struct {
	download.GuardStatus
	// SharedWith lists the library folders that measure as the same filesystem as the
	// downloads folder. The guard still functions, but it is then watching the whole
	// array rather than a torrent drive, so a threshold tuned for a cache pool is
	// measuring the wrong thing entirely.
	SharedWith []health.Folder `json:"shared_with"`
	// SharedWithLibrary is len(SharedWith) > 0, kept for one release for a cached UI.
	SharedWithLibrary bool `json:"shared_with_library"`
}

// handleDiskGuardStatus reports what the disk guard is watching and what it sees.
// The setting depends entirely on the downloads folder (lib_downloads_dir from Settings →
// Library, with ARRMADA_DOWNLOADS_DIR as the fallback) pointing at the torrent
// drive, and there is no way to know that from inside the app — so show the resolved
// path and the reading taken from it, and let the user confirm it themselves.
func (a *api) handleDiskGuardStatus(w http.ResponseWriter, r *http.Request) {
	if a.deps.DiskGuard == nil {
		a.writeError(w, http.StatusServiceUnavailable, "the disk guard isn't running")
		return
	}
	ctx := r.Context()
	out := diskGuardStatus{GuardStatus: a.deps.DiskGuard.Status(ctx)}
	out.SharedWith = a.sharedWithDownloads(ctx, out.Path)
	out.SharedWithLibrary = len(out.SharedWith) > 0
	a.writeJSON(w, http.StatusOK, out)
}

// sharedWithDownloads lists the library folders the user picked that sit on the same
// filesystem as the downloads folder the guard watches. It used to compare against
// ARRMADA_LIBRARY_DIR — the managed volume — and so reported the wrong disk on any
// install whose libraries live on the array.
func (a *api) sharedWithDownloads(ctx context.Context, downloads string) []health.Folder {
	shared := []health.Folder{}
	if downloads == "" {
		return shared
	}
	for _, f := range health.LibraryFolders(a.pickedConfig(ctx), a.booksEnabled(ctx), a.musicEnabled(ctx)) {
		if f.Role == "downloads" {
			continue
		}
		if same, ok := health.SameFilesystem(downloads, f.Path); ok && same {
			shared = append(shared, f)
		}
	}
	return shared
}
