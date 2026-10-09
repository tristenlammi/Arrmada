package httpapi

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/movies"
	"github.com/tristenlammi/arrmada/internal/quality"
)

func (a *api) handleListMovies(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Movies.List(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list movies")
		return
	}
	if list == nil {
		list = []movies.Movie{}
	}
	// Attach live download progress so the grid can show an indicator. When the client
	// can't be read, the grid says the status is unknown rather than "Wanted".
	snap, queueKnown, _ := a.queueSnapshot(r.Context())
	queue := snap.Items
	// Joined through the acquisition record by info hash: one query and one pass, and a
	// torrent named nothing like the film still shows on its poster.
	var acqs map[int64][]automation.Acquisition
	if a.deps.Automation != nil && len(queue) > 0 {
		acqs, _ = a.deps.Automation.ActiveByItem(r.Context(), "movie")
	}
	byHash := queueByHash(queue)
	var stale []int64
	for i := range list {
		if len(acqs[list[i].ID]) > 0 {
			list[i].Download = movieDownload(list[i], acqs[list[i].ID], byHash, queue)
		}
		if list[i].MediaStale() {
			stale = append(stale, list[i].ID)
		}
	}
	// Backfill media info cached before it existed or by an older probe: one job for the
	// lot, not a goroutine per movie on every 4-second poll. Single-flight makes repeat
	// polls free while it runs, and a movie it already tried waits an hour before the
	// next attempt (a file ffprobe can't read stays stale).
	if pending := a.deps.Movies.StaleMediaPending(stale); len(pending) > 0 {
		_, _, _ = a.submit(r, jobs.Spec{Kind: "movie.media-backfill", Target: "all", Class: "movie.media-backfill", Timeout: 10 * time.Minute,
			Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
				n := a.deps.Movies.BackfillStaleMedia(ctx, pending)
				p.SetMessage(fmt.Sprintf("Read media info for %d movies", n))
				return map[string]int{"movies": n}, nil
			}})
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"movies":             list,
		"metadata_available": a.deps.Movies.MetadataAvailable(),
		"client_health":      queueHealth{OK: queueKnown},
	})
}

func (a *api) handleLookupMovies(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query().Get("q")
	if q == "" {
		a.writeError(w, http.StatusBadRequest, "missing ?q= query")
		return
	}
	if !a.deps.Movies.MetadataAvailable() {
		a.writeError(w, http.StatusBadRequest, "movie metadata isn't configured — add a TMDB key in Settings → System → API keys (free from themoviedb.org)")
		return
	}
	results, err := a.deps.Movies.Lookup(r.Context(), q)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

type addMovieRequest struct {
	TMDBID         int    `json:"tmdb_id"`
	QualityProfile string `json:"quality_profile"`
	Monitored      *bool  `json:"monitored"`
	SearchOnAdd    *bool  `json:"search_on_add"`
}

func (a *api) handleAddMovie(w http.ResponseWriter, r *http.Request) {
	var req addMovieRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.TMDBID == 0 {
		a.writeError(w, http.StatusBadRequest, "tmdb_id is required")
		return
	}
	monitored := true
	if req.Monitored != nil {
		monitored = *req.Monitored
	}
	// "Search on add" — default from the persisted preference, overridable per-add.
	searchOnAdd := a.deps.Settings.GetBool(r.Context(), keySearchOnAdd, true)
	if req.SearchOnAdd != nil {
		searchOnAdd = *req.SearchOnAdd
	}
	// Off means "just add it, don't go get it." A monitored-and-missing movie
	// would be grabbed by the periodic search/RSS sweeps regardless of the
	// immediate search below — so honor the intent by adding it UNMONITORED. The
	// user can monitor it later to start searching.
	if !searchOnAdd {
		monitored = false
	}
	// Fall back to the user's chosen default profile for this media type.
	if req.QualityProfile == "" {
		req.QualityProfile = a.deps.Quality.DefaultProfile(r.Context(), "movie")
	}

	m, err := a.deps.Movies.Add(r.Context(), req.TMDBID, req.QualityProfile, monitored)
	if errors.Is(err, movies.ErrExists) {
		a.writeError(w, http.StatusConflict, "that movie is already in your library")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}

	if m.Monitored && searchOnAdd {
		_, _, _ = a.submit(r, triggered(automation.TriggerAdd, a.movieSearchJob(m.ID)))
	}

	a.writeJSON(w, http.StatusCreated, m)
}

// handleSearchMovie triggers a search+grab for one movie (manual "search now").
func (a *api) handleSearchMovie(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	// Run in the background; searching indexers (via FlareSolverr) is slow. A second
	// click while it runs gets the same job back. The button's search leaves a movie
	// that is already downloading alone (SearchMovieManual).
	started := time.Now().UnixMilli()
	jobID, existing, ok := a.submitOr503(w, r, a.movieManualSearchJob(id))
	if !ok {
		return
	}
	// started_at_ms lets a page without the job (or the socket) find this search's
	// stored attempt: GET /api/v1/searches?since=.
	a.accepted(w, jobID, existing, map[string]any{"status": "searching", "started_at_ms": started})
}

// handleListBlocklist returns a movie's blocklisted releases.
func (a *api) handleListBlocklist(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	entries, err := a.deps.Automation.Blocklisted(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load blocklist")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"blocklist": entries})
}

// blocklistRequest names the release to block by its token from this movie's
// interactive search, or by title (and optionally indexer) alone: blocklist entries are
// keyed by title, so a title-only block needs nothing from the indexer.
type blocklistRequest struct {
	Token       string `json:"token"`
	Title       string `json:"title"`
	Indexer     string `json:"indexer"`
	SearchAgain bool   `json:"search_again"`
}

// handleBlocklist adds a release to a movie's blocklist, optionally re-searching.
func (a *api) handleBlocklist(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req blocklistRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	// The release link is kept on the block row (it's what the release was), but only
	// ever one this server handed out under a token — never one the browser supplies.
	downloadURL := ""
	if req.Token != "" {
		ref, ok := a.resolveRelease(w, r, req.Token, automation.ReleaseKindMovie, id)
		if !ok {
			return
		}
		req.Title, req.Indexer, downloadURL = ref.Title, ref.Indexer, ref.DownloadURL
	}
	if req.Title == "" {
		a.writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if req.SearchAgain {
		// The same job kind as a plain search: blocklisting and searching while a search
		// of this movie runs would only race it.
		spec := a.movieSearchJob(id)
		spec.Fn = outcomeFn("movie", func(ctx context.Context) (automation.SearchOutcome, error) {
			return a.deps.Automation.BlocklistAndSearch(ctx, id, req.Title, req.Indexer, downloadURL)
		})
		jobID, existing, ok := a.submitOr503(w, r, spec)
		if !ok {
			return
		}
		a.accepted(w, jobID, existing, map[string]any{"status": "blocklisted, searching"})
		return
	}
	if err := a.deps.Automation.Blocklist(r.Context(), id, req.Title, req.Indexer, downloadURL, "manually blocklisted"); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not blocklist")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "blocklisted"})
}

// handleUnblock removes a blocklist entry.
func (a *api) handleUnblock(w http.ResponseWriter, r *http.Request) {
	bid, ok := a.pathValueID(w, r, "bid")
	if !ok {
		return
	}
	if err := a.deps.Automation.Unblock(r.Context(), bid); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not remove blocklist entry")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// movieScanSummary is a movie scan's job result: the shared counts plus the files it
// attached to films already in the library and the folders it left alone as duplicates.
type movieScanSummary struct {
	scanSummary
	Attached   int `json:"attached"`
	Duplicates int `json:"duplicates"`
}

// movieScanMessage words a finished movie scan for the toast.
func movieScanMessage(res movies.ScanResult) string {
	msg := scanMessage("movie", res.Imported, len(res.Unmatched))
	if res.Attached > 0 {
		if res.Imported == 0 && len(res.Unmatched) == 0 {
			msg = "Attached " + countOf(res.Attached, "file") + " to movies already in the library"
		} else {
			msg += "; attached " + countOf(res.Attached, "file") + " to movies already in the library"
		}
	}
	if n := len(res.Duplicates); n > 0 {
		msg += "; " + countOf(n, "folder") + " left alone (the movie already has a different file)"
	}
	return msg
}

// handleScanLibrary scans the library folder for existing movie files and catalogs them
// — unmonitored on profile n/a, unless the body asks to monitor them on a profile — and
// attaches files to films already in the library without one. Runs in the background
// because TMDB lookups over a large library take a while.
func (a *api) handleScanLibrary(w http.ResponseWriter, r *http.Request) {
	var opts movies.ScanOptions
	if r.ContentLength > 0 && !a.decodeJSON(w, r, &opts) {
		return
	}
	opts.QualityProfile = strings.TrimSpace(opts.QualityProfile)
	if opts.Monitor && opts.QualityProfile == "" && a.deps.Quality != nil {
		opts.QualityProfile = a.deps.Quality.DefaultProfile(r.Context(), "movie")
	}
	// The same check as Automation.KnownProfile, which is the quality service's.
	if opts.QualityProfile != "" && (a.deps.Quality == nil || !a.deps.Quality.Known(r.Context(), opts.QualityProfile)) {
		a.writeError(w, http.StatusBadRequest, "unknown quality profile")
		return
	}
	root := a.libMovies(r) // resolve the configured folder before we detach
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "movie.scan", Target: "all", Class: jobs.ClassLibraryScan, Timeout: 15 * time.Minute,
		Fn: func(ctx context.Context, p *jobs.Progress) (any, error) {
			res, err := a.deps.Movies.ScanLibrary(ctx, root, opts)
			if err != nil {
				return nil, err
			}
			a.deps.Log.Info("library scan complete", "imported", res.Imported, "attached", res.Attached, "duplicates", len(res.Duplicates),
				"skipped", res.Skipped, "unmatched", len(res.Unmatched))
			a.deps.Bus.Publish("library.scanned", map[string]any{"media": "movie", "imported": res.Imported, "unmatched": len(res.Unmatched),
				"attached": res.Attached, "duplicates": len(res.Duplicates)})
			p.SetMessage(movieScanMessage(res))
			return movieScanSummary{
				scanSummary: scanSummary{Imported: res.Imported, Skipped: res.Skipped, Unmatched: len(res.Unmatched)},
				Attached:    res.Attached, Duplicates: len(res.Duplicates),
			}, nil
		}})
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "scanning"})
}

// handleMovieUnmatched returns the folders the last scan couldn't identify, each
// with candidate matches to choose from.
func (a *api) handleMovieUnmatched(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, map[string]any{"unmatched": a.deps.Movies.LastUnmatched()})
}

// handleMovieImportFolder catalogs one library folder as an explicitly chosen
// TMDB movie — the manual pick for a folder the scan couldn't identify.
func (a *api) handleMovieImportFolder(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Folder string `json:"folder"`
		TMDBID int    `json:"tmdb_id"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if !safeFolder(req.Folder) || req.TMDBID == 0 {
		a.writeError(w, http.StatusBadRequest, "folder and tmdb_id are required")
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()
	if err := a.deps.Movies.ImportFolderAs(ctx, a.libMovies(r), req.Folder, req.TMDBID); err != nil {
		if errors.Is(err, movies.ErrExists) {
			a.writeError(w, http.StatusConflict, err.Error())
			return
		}
		a.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "imported"})
}

// safeFolder rejects a folder name that could escape the library root.
func safeFolder(f string) bool {
	return f != "" && f != "." && !strings.ContainsAny(f, `/\`) && !strings.Contains(f, "..")
}

// handleGetMovie returns a single movie for the detail page.
func (a *api) handleGetMovie(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	m, err := a.deps.Movies.Get(r.Context(), id)
	if errors.Is(err, movies.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "movie not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load movie")
		return
	}
	// One live read of the tracks: the default track's File is the movie's file. Asking
	// for the file and the versions separately probed the default file twice per load.
	if versions, verr := a.deps.Movies.VersionsLive(r.Context(), id); verr == nil {
		m.Versions = versions
		m.File = versions[0].File
	}
	// What's in flight for it, by the acquisition record (ACQ-25), joined to the queue by
	// info hash: the progress bar and the Acquisition card read the same grabs, whatever
	// the torrent is called.
	var acqs []automation.Acquisition
	var queue []download.Item
	if a.deps.Automation != nil {
		acqs, _ = a.deps.Automation.Active(r.Context(), "movie", m.ID)
	}
	if a.deps.Downloads != nil && len(acqs) > 0 {
		queue, _ = a.deps.Downloads.Queue(r.Context())
	}
	byHash := queueByHash(queue)
	m.Download = movieDownload(m, acqs, byHash, queue)
	m.UpgradesAllowed = upgradeWatched(m.Monitored, m.HasFile, a.anyVersionUpgrades(r.Context(), &m))
	// What searching has come to: the last search's result, the sweep's backoff and when
	// it will next look (search_attempts), beside the movie's own fields.
	var search automation.SearchState
	if a.deps.Automation != nil {
		search = a.deps.Automation.MovieSearchState(r.Context(), m)
	}
	m.Acquisition = a.movieAcquisition(r.Context(), &m, acqs, byHash, queue)
	a.writeJSON(w, http.StatusOK, struct {
		movies.Movie
		automation.SearchState
	}{m, search})
}

// movieAcquisition gathers what the Acquisition card states, from the facts the sweeps
// act on: monitoring, the profile and whether it upgrades, availability, an in-flight
// grab, and a recorded file that's gone. m carries its live tracks and UpgradesAllowed.
func (a *api) movieAcquisition(ctx context.Context, m *movies.Movie, acqs []automation.Acquisition, byHash map[string]download.Item, queue []download.Item) *movies.Acquisition {
	acq := &movies.Acquisition{
		Monitored:       m.Monitored,
		UpgradesAllowed: m.UpgradesAllowed,
		ScannedIn:       m.QualityProfile == "n/a",
		Available:       a.deps.Movies.IsAvailable(*m),
		FileMissing:     m.HasFile && m.File != nil && m.File.Missing,
	}
	acq.ProfileKnown = !acq.ScannedIn && m.QualityProfile != "" && a.deps.Quality != nil && a.deps.Quality.Known(ctx, m.QualityProfile)
	if m.Extra != nil {
		acq.AvailableFrom = m.Extra.ReleaseDate
	}
	// In flight by the acquisition record — an upgrade for a film that has its file
	// included — with the live progress when its torrent is in the queue.
	if len(acqs) > 0 {
		acq.Downloading = true
		acq.DownloadTitle, acq.DownloadProgress = acqs[0].Title, acqs[0].Progress
		if it, ok := automation.QueueItemFor(acqs[0], byHash, queue); ok {
			acq.DownloadProgress = it.Progress
		}
	}
	return acq
}

// upgradeWatched mirrors the upgrade sweep's own filter (UpgradeMovies skips a movie that
// isn't monitored or has no file; upgradeMovie then needs a version whose profile has
// upgrades on), so the detail page only promises upgrade watching when the sweep will look.
func upgradeWatched(monitored, hasFile, profileAllows bool) bool {
	return monitored && hasFile && profileAllows
}

// anyVersionUpgrades reports whether at least one of the movie's monitored versions with a
// file sits on a profile that upgrades, resolved the way the sweep resolves it (a dangling
// or "n/a" profile falls back to the default).
func (a *api) anyVersionUpgrades(ctx context.Context, m *movies.Movie) bool {
	if a.deps.Quality == nil {
		return false
	}
	allows := func(profile string) bool {
		return a.deps.Quality.AllowsUpgrades(ctx, a.deps.Quality.Effective(ctx, profile, quality.MediaMovie))
	}
	if len(m.Versions) == 0 {
		return allows(m.QualityProfile)
	}
	for _, v := range m.Versions {
		if v.Monitored && v.HasFile && allows(v.QualityProfile) {
			return true
		}
	}
	return false
}

// handleMovieCollection returns the movie's TMDB collection members, each
// flagged with whether it's already in the library — powers "add whole collection".
func (a *api) handleMovieCollection(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	m, err := a.deps.Movies.Get(r.Context(), id)
	if errors.Is(err, movies.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "movie not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load movie")
		return
	}
	if m.Extra == nil || m.Extra.CollectionID == 0 {
		a.writeJSON(w, http.StatusOK, map[string]any{"name": "", "members": []movies.CollectionMember{}})
		return
	}
	name, members, err := a.deps.Movies.Collection(r.Context(), m.Extra.CollectionID)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, "could not load collection")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"name": name, "members": members})
}

// handleListVersions returns a movie's version tracks.
func (a *api) handleListVersions(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	versions, err := a.deps.Movies.VersionsLive(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load versions")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"versions": versions})
}

type versionRequest struct {
	Label          string `json:"label"`
	QualityProfile string `json:"quality_profile"`
	Edition        string `json:"edition"`
	Monitored      *bool  `json:"monitored"`
}

// handleAddVersion adds an opt-in extra version track to a movie.
func (a *api) handleAddVersion(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req versionRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.QualityProfile == "" {
		req.QualityProfile = a.deps.Quality.DefaultProfile(r.Context(), "movie")
	}
	if req.QualityProfile != "" && !a.deps.Automation.KnownProfile(r.Context(), req.QualityProfile) {
		a.writeError(w, http.StatusBadRequest, "unknown quality profile")
		return
	}
	monitored := true
	if req.Monitored != nil {
		monitored = *req.Monitored
	}
	v, err := a.deps.Movies.AddVersion(r.Context(), id, req.Label, req.QualityProfile, req.Edition, monitored)
	if errors.Is(err, movies.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "movie not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not add version")
		return
	}
	if monitored {
		_, _, _ = a.submit(r, triggered(automation.TriggerAdd, a.movieSearchJob(id)))
	}
	a.writeJSON(w, http.StatusCreated, v)
}

// handleUpdateVersion edits an extra version's fields.
func (a *api) handleUpdateVersion(w http.ResponseWriter, r *http.Request) {
	vid, ok := a.pathValueID(w, r, "vid")
	if !ok {
		return
	}
	var req versionRequest
	if !a.decodeJSON(w, r, &req) {
		return
	}
	monitored := true
	if req.Monitored != nil {
		monitored = *req.Monitored
	}
	if err := a.deps.Movies.UpdateVersion(r.Context(), vid, req.Label, req.QualityProfile, req.Edition, monitored); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update version")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "updated"})
}

// handleDeleteVersion removes an extra version track (and its file).
func (a *api) handleDeleteVersion(w http.ResponseWriter, r *http.Request) {
	vid, ok := a.pathValueID(w, r, "vid")
	if !ok {
		return
	}
	if err := a.deps.Movies.DeleteVersion(r.Context(), vid); err != nil {
		if a.writeBinRefusal(w, err) {
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not delete version")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleDeleteVersionFile deletes a version's file (vid 0 = the default track).
func (a *api) handleDeleteVersionFile(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	vid, ok := a.pathValueID(w, r, "vid")
	if !ok {
		return
	}
	if err := a.deps.Movies.DeleteVersionFile(r.Context(), id, vid); err != nil {
		if a.writeBinRefusal(w, err) {
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not delete file")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleSetProfile changes a movie's quality profile. If the movie is monitored
// and still missing, it re-searches under the new criteria in the background.
func (a *api) handleSetProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		QualityProfile string `json:"quality_profile"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if !a.deps.Automation.KnownProfile(r.Context(), req.QualityProfile) {
		a.writeError(w, http.StatusBadRequest, "unknown quality profile")
		return
	}
	if err := a.deps.Movies.SetQualityProfile(r.Context(), id, req.QualityProfile); err != nil {
		if errors.Is(err, movies.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "movie not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not update quality profile")
		return
	}

	// React to the profile change based on the movie's current state.
	resp := map[string]any{"quality_profile": req.QualityProfile, "downgrade": false}
	if m, err := a.deps.Movies.Get(r.Context(), id); err == nil && m.Monitored {
		rej, rejected := quality.Rejection{}, false
		if m.HasFile {
			rej, rejected = a.deps.Quality.WouldRejectReason(r.Context(), req.QualityProfile, a.deps.Automation.CurrentMovieFile(r.Context(), m))
		}
		switch {
		case !m.HasFile:
			// Missing → search under the new criteria.
			_, _, _ = a.submit(r, a.movieSearchJob(id))
		case rejected:
			// The existing file no longer fits the new profile. Don't act automatically;
			// let the UI ask, and say whether a smaller release fixes it (the file is above
			// a ceiling) or it needs a different kind of release altogether.
			kind := "different"
			if rej.Ceiling != "" {
				kind = "smaller"
			}
			resp["downgrade"] = true
			resp["downgrade_reason"] = rej.Reason
			resp["downgrade_kind"] = kind
			resp["downgrade_ceiling"] = rej.Ceiling
		default:
			// The file still fits → look for a better release under the new profile.
			_, _, _ = a.submit(r, jobs.Spec{Kind: "movie.upgrade", Target: jobTarget("movie", id), Class: jobs.ClassIndexerSearch, Timeout: 3 * time.Minute,
				Fn: interactive(searchFn(func(ctx context.Context) error { return a.deps.Automation.UpgradeMovie(ctx, id) }))})
		}
	}
	a.writeJSON(w, http.StatusOK, resp)
}

// handleRegrab deliberately re-grabs a movie under its current profile even
// though it has a file — the "download smaller version" action behind a
// downgrade prompt. Replaces the file on import.
func (a *api) handleRegrab(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	jobID, existing, ok := a.submitOr503(w, r, jobs.Spec{Kind: "movie.regrab", Target: jobTarget("movie", id), Class: jobs.ClassIndexerSearch, Timeout: 3 * time.Minute,
		Fn: interactive(errFn(func(ctx context.Context) error { return a.deps.Automation.RegrabMovie(ctx, id) }))})
	if !ok {
		return
	}
	a.accepted(w, jobID, existing, map[string]any{"status": "searching"})
}

// handleDeleteMovieFile deletes a movie's file from disk (flipping it back to
// Wanted) without removing the movie from the library.
func (a *api) handleDeleteMovieFile(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if err := a.deps.Movies.DeleteFile(r.Context(), id); err != nil {
		if errors.Is(err, movies.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "movie not found")
			return
		}
		if a.writeBinRefusal(w, err) {
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not delete file")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleForgetMissingFile clears the record of a track whose file is gone from disk. It
// never deletes anything: a file that's back on disk answers 409 and stays recorded.
func (a *api) handleForgetMissingFile(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		VersionID int64 `json:"version_id"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Movies.ForgetMissingFile(r.Context(), id, req.VersionID); err != nil {
		switch {
		case errors.Is(err, movies.ErrNotFound):
			a.writeError(w, http.StatusNotFound, "movie not found")
		case errors.Is(err, movies.ErrFileExists):
			a.writeError(w, http.StatusConflict, "the file is back on disk — refresh instead")
		default:
			a.writeError(w, http.StatusInternalServerError, err.Error())
		}
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "cleared"})
}

// handleMovieReleases runs an interactive search and returns ranked releases
// (best first) without grabbing — the user picks one to grab via /grab.
func (a *api) handleMovieReleases(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(indexer.WithInteractive(r.Context()), 90*time.Second)
	defer cancel()
	list, err := a.deps.Automation.RankReleases(ctx, id)
	if errors.Is(err, movies.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "movie not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.tokenize(r, &list, automation.ReleaseRef{MediaKind: automation.ReleaseKindMovie, MediaID: id})
	a.writeJSON(w, http.StatusOK, list)
}

// handleSetMonitored toggles a movie's monitoring from the detail page.
func (a *api) handleSetMonitored(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Monitored bool `json:"monitored"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Movies.SetMonitored(r.Context(), id, req.Monitored); err != nil {
		if errors.Is(err, movies.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "movie not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not update monitoring")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"monitored": req.Monitored})
}

// handleRefreshMovie re-pulls metadata and rescans the disk for a movie.
func (a *api) handleRefreshMovie(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	m, err := a.deps.Movies.Refresh(ctx, id)
	if errors.Is(err, movies.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "movie not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not refresh movie")
		return
	}
	if versions, verr := a.deps.Movies.VersionsLive(ctx, id); verr == nil {
		m.Versions = versions
		m.File = versions[0].File
	}
	a.deps.Bus.Publish("movie.refreshed", map[string]any{"id": id})
	a.writeJSON(w, http.StatusOK, m)
}

// handleMovieHistory returns a movie's activity timeline.
func (a *api) handleMovieHistory(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	events, err := a.deps.Movies.Events(r.Context(), id, 100)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read history")
		return
	}
	if events == nil {
		events = []movies.Event{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

// handleSetAvailability changes a movie's minimum-availability threshold.
func (a *api) handleSetAvailability(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		MinAvailability string `json:"min_availability"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Movies.SetMinAvailability(r.Context(), id, req.MinAvailability); err != nil {
		if errors.Is(err, movies.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "movie not found")
			return
		}
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"min_availability": req.MinAvailability})
}

// handleManualImportList lists importable video files under the downloads dir
// (or ?path=). handleManualImport imports a chosen one.
func (a *api) handleManualImportList(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.pathID(w, r); !ok {
		return
	}
	dir, err := a.checkImportPath(r.Context(), r.URL.Query().Get("path"))
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx, cancel := importListContext(r)
	defer cancel()
	cands, truncated, err := a.deps.Movies.ManualImportCandidates(ctx, dir, library.ListMaxResults)
	if err != nil && ctx.Err() == nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if cands == nil {
		cands = []movies.ImportCandidate{}
	}
	a.writeImportList(w, r, ctx, "movies", dir, cands, truncated)
}

func (a *api) handleManualImport(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Path string `json:"path"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Path == "" {
		a.writeError(w, http.StatusBadRequest, "path is required")
		return
	}
	src, err := a.checkImportPath(r.Context(), req.Path)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := a.deps.Movies.ManualImport(r.Context(), id, src); err != nil {
		if errors.Is(err, movies.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "movie not found")
			return
		}
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "imported"})
}

// handleRenamePreview returns the proposed canonical name; handleRename applies it.
func (a *api) handleRenamePreview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	current, proposed, matches, err := a.deps.Movies.RenamePreview(r.Context(), id)
	if errors.Is(err, movies.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "movie not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not preview rename")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"current": current, "proposed": proposed, "matches": matches})
}

func (a *api) handleRename(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if err := a.deps.Movies.Rename(r.Context(), id); err != nil {
		if errors.Is(err, movies.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "movie not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "renamed"})
}

// handleMovieDeletePreview says what deleting a movie with its files would move and where
// it would go, so the dialog can state it before anything happens.
func (a *api) handleMovieDeletePreview(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	plan, err := a.deps.Movies.DeletePlan(r.Context(), id)
	if err != nil {
		if errors.Is(err, movies.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "movie not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not list the movie's files")
		return
	}
	pending := []automation.PendingMovieDownload{}
	if a.deps.Automation != nil {
		if p, err := a.deps.Automation.PendingMovieDownloads(r.Context(), id); err == nil {
			pending = p
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"versions": plan.Versions, "sidecars": plan.Sidecars, "bytes": plan.Bytes,
		"recycle":           a.recycleMode(r.Context()),
		"pending_downloads": pending,
	})
}

func (a *api) handleDeleteMovie(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	opts := automation.DeleteMovieOpts{
		DeleteFiles:     r.URL.Query().Get("delete_files") == "true",
		CancelDownloads: r.URL.Query().Get("cancel_downloads") == "true",
	}
	// Detached from the request: moving files to a bin on another disk is a copy, and a
	// browser giving up part-way must not leave the files binned while the movie stays.
	ctx := context.WithoutCancel(r.Context())
	var err error
	if a.deps.Automation != nil {
		// Settles the movie's downloads too, so a finished one can't re-import it.
		err = a.deps.Automation.DeleteMovie(ctx, id, opts)
	} else {
		err = a.deps.Movies.Delete(ctx, id, opts.DeleteFiles)
	}
	if err != nil {
		if errors.Is(err, movies.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "movie not found")
			return
		}
		if a.writeBinRefusal(w, err) {
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not delete movie")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
