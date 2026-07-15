package httpapi

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/tristenlammi/arrmada/internal/movies"
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
		// Backfill media info for movies imported before caching (fire-and-forget).
		if list[i].HasFile && list[i].File == nil {
			go a.deps.Movies.EnsureMedia(context.Background(), list[i].ID)
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
		a.writeError(w, http.StatusBadRequest, "movie metadata isn't configured — set ARRMADA_TMDB_API_KEY (a free key from themoviedb.org)")
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
		go func(id int64) {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			if err := a.deps.Automation.SearchMovie(ctx, id); err != nil {
				a.deps.Log.Warn("auto-search on add failed", "movie", m.Title, "err", err)
			}
		}(m.ID)
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
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		if err := a.deps.Automation.SearchMovie(ctx, id); err != nil {
			a.deps.Log.Warn("manual movie search failed", "id", id, "err", err)
		}
	}()
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
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			if err := a.deps.Automation.BlocklistAndSearch(ctx, id, req.Title, req.Indexer, req.DownloadURL); err != nil {
				a.deps.Log.Warn("blocklist & search failed", "id", id, "err", err)
			}
		}()
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
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
		defer cancel()
		res, err := a.deps.Movies.ScanLibrary(ctx)
		if err != nil {
			a.deps.Log.Warn("library scan failed", "err", err)
			return
		}
		a.deps.Log.Info("library scan complete", "imported", res.Imported, "skipped", res.Skipped, "unmatched", len(res.Unmatched))
		a.deps.Bus.Publish("library.scanned", map[string]any{"imported": res.Imported, "unmatched": res.Unmatched})
	}()
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "scanning"})
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
	if file, ferr := a.deps.Movies.FileInfo(r.Context(), id); ferr == nil {
		m.File = file
	}
	if versions, verr := a.deps.Movies.Versions(r.Context(), id); verr == nil {
		m.Versions = versions
	}
	if queue, qerr := a.deps.Downloads.Queue(r.Context()); qerr == nil {
		m.Download = downloadFor(queue, m)
	}
	a.writeJSON(w, http.StatusOK, m)
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
	versions, err := a.deps.Movies.Versions(r.Context(), id)
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
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			_ = a.deps.Automation.SearchMovie(ctx, id)
		}()
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
			go a.bg(func(ctx context.Context) error { return a.deps.Automation.SearchMovie(ctx, id) }, "re-search after profile change", id)
		case a.deps.Quality.WouldReject(r.Context(), req.QualityProfile, m.SourceRelease, sizeGB(m)):
			// The existing file no longer fits the new (lower) profile → this is a
			// downgrade. Don't act automatically; let the UI ask the user.
			downgrade = true
		default:
			// The file still fits → look for a better release under the new profile.
			go a.bg(func(ctx context.Context) error { return a.deps.Automation.UpgradeMovie(ctx, id) }, "upgrade after profile change", id)
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
	go a.bg(func(ctx context.Context) error { return a.deps.Automation.RegrabMovie(ctx, id) }, "regrab", id)
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "searching"})
}

// bg runs fn in the background with a bounded timeout, logging failures.
func (a *api) bg(fn func(context.Context) error, what string, id int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	if err := fn(ctx); err != nil {
		a.deps.Log.Warn(what+" failed", "id", id, "err", err)
	}
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
	if file, ferr := a.deps.Movies.FileInfo(ctx, id); ferr == nil {
		m.File = file
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
	dir := r.URL.Query().Get("path")
	if dir == "" {
		dir = a.deps.Config.DownloadsDir
	}
	cands, err := a.deps.Movies.ManualImportCandidates(dir)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if cands == nil {
		cands = []movies.ImportCandidate{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"path": dir, "candidates": cands})
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
	if err := a.deps.Movies.ManualImport(r.Context(), id, req.Path); err != nil {
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

func (a *api) handleDeleteMovie(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	deleteFiles := r.URL.Query().Get("delete_files") == "true"
	if err := a.deps.Movies.Delete(r.Context(), id, deleteFiles); err != nil {
		if errors.Is(err, movies.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "movie not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not delete movie")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
