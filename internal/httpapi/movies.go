package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/automation"
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
	// Attach live download progress so the grid can show an indicator.
	queue, _ := a.deps.Downloads.Queue(r.Context())
	for i := range list {
		if len(queue) > 0 {
			list[i].Download = downloadFor(queue, list[i])
		}
		// Backfill media info cached before it existed or by an older probe (fire-and-forget,
		// bounded and deduplicated inside EnsureMedia).
		if list[i].MediaStale() {
			id := list[i].ID
			a.bg("media backfill", idTarget("movie", id), 10*time.Minute, func(ctx context.Context) error {
				a.deps.Movies.EnsureMedia(ctx, id)
				return nil
			})
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"movies":             list,
		"metadata_available": a.deps.Movies.MetadataAvailable(),
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
		id := m.ID
		a.bg("auto-search on add", idTarget("movie", id), 3*time.Minute, func(ctx context.Context) error {
			return a.deps.Automation.SearchMovie(ctx, id)
		})
	}

	a.writeJSON(w, http.StatusCreated, m)
}

// handleSearchMovie triggers a search+grab for one movie (manual "search now").
func (a *api) handleSearchMovie(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	// Run in the background; searching indexers (via FlareSolverr) is slow.
	a.bg("manual movie search", idTarget("movie", id), 3*time.Minute, func(ctx context.Context) error {
		return a.deps.Automation.SearchMovie(ctx, id)
	})
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "searching"})
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

type blocklistRequest struct {
	Title       string `json:"title"`
	Indexer     string `json:"indexer"`
	DownloadURL string `json:"download_url"`
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
	if req.Title == "" {
		a.writeError(w, http.StatusBadRequest, "title is required")
		return
	}
	if req.SearchAgain {
		a.bg("blocklist & search", idTarget("movie", id), 3*time.Minute, func(ctx context.Context) error {
			return a.deps.Automation.BlocklistAndSearch(ctx, id, req.Title, req.Indexer, req.DownloadURL)
		})
		a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "blocklisted, searching"})
		return
	}
	if err := a.deps.Automation.Blocklist(r.Context(), id, req.Title, req.Indexer, req.DownloadURL, "manually blocklisted"); err != nil {
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

// handleScanLibrary scans the library folder for existing movie files and
// catalogs them (unmonitored, n/a profile). Runs in the background because TMDB
// lookups over a large library take a while.
func (a *api) handleScanLibrary(w http.ResponseWriter, r *http.Request) {
	root := a.libMovies(r) // resolve the configured folder before we detach
	a.bg("library scan", "movies", 15*time.Minute, func(ctx context.Context) error {
		res, err := a.deps.Movies.ScanLibrary(ctx, root)
		if err != nil {
			return err
		}
		a.deps.Log.Info("library scan complete", "imported", res.Imported, "skipped", res.Skipped, "unmatched", len(res.Unmatched))
		a.deps.Bus.Publish("library.scanned", map[string]any{"media": "movie", "imported": res.Imported, "unmatched": len(res.Unmatched)})
		return nil
	})
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "scanning"})
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
	if queue, qerr := a.deps.Downloads.Queue(r.Context()); qerr == nil {
		m.Download = downloadFor(queue, m)
	}
	m.UpgradesAllowed = upgradeWatched(m.Monitored, m.HasFile, a.anyVersionUpgrades(r.Context(), &m))
	a.writeJSON(w, http.StatusOK, m)
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
		a.bg("search for new version", idTarget("movie", id), 3*time.Minute, func(ctx context.Context) error {
			return a.deps.Automation.SearchMovie(ctx, id)
		})
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
	a.deps.Bus.Publish("movie.file_deleted", map[string]any{"id": id})
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
	downgrade := false
	if m, err := a.deps.Movies.Get(r.Context(), id); err == nil && m.Monitored {
		switch {
		case !m.HasFile:
			// Missing → search under the new criteria.
			a.bg("re-search after profile change", idTarget("movie", id), 3*time.Minute, func(ctx context.Context) error {
				return a.deps.Automation.SearchMovie(ctx, id)
			})
		case a.deps.Quality.WouldReject(r.Context(), req.QualityProfile, m.SourceRelease, sizeGB(m), m.Runtime):
			// The existing file no longer fits the new (lower) profile → this is a
			// downgrade. Don't act automatically; let the UI ask the user.
			downgrade = true
		default:
			// The file still fits → look for a better release under the new profile.
			a.bg("upgrade after profile change", idTarget("movie", id), 3*time.Minute, func(ctx context.Context) error {
				return a.deps.Automation.UpgradeMovie(ctx, id)
			})
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"quality_profile": req.QualityProfile, "downgrade": downgrade})
}

// handleRegrab deliberately re-grabs a movie under its current profile even
// though it has a file — the "download smaller version" action behind a
// downgrade prompt. Replaces the file on import.
func (a *api) handleRegrab(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	a.bg("regrab", idTarget("movie", id), 3*time.Minute, func(ctx context.Context) error {
		return a.deps.Automation.RegrabMovie(ctx, id)
	})
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "searching"})
}

// sizeGB returns a movie's on-disk file size in GB (0 if unknown).
func sizeGB(m movies.Movie) float64 {
	if m.File != nil && m.File.SizeBytes > 0 {
		return float64(m.File.SizeBytes) / (1024 * 1024 * 1024)
	}
	return 0
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
	a.deps.Bus.Publish("movie.file_deleted", map[string]any{"id": id})
	w.WriteHeader(http.StatusNoContent)
}

// handleMovieReleases runs an interactive search and returns ranked releases
// (best first) without grabbing — the user picks one to grab via /grab.
func (a *api) handleMovieReleases(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 90*time.Second)
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
	a.deps.Bus.Publish("movie.downloaded", map[string]any{"id": id})
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
	a.deps.Bus.Publish("movie.renamed", map[string]any{"id": id})
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
