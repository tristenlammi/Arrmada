package httpapi

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tristenlammi/arrmada/internal/config"
	"github.com/tristenlammi/arrmada/internal/libroots"
)

// First-run setup. The installer used to ask for the TMDB key in a terminal and write
// library paths guessed from one person's folder layout (media/movies, torrents…). Now
// it only mounts the media folder, and the app walks an admin through the rest: the
// metadata key, then each library folder with the folder picker, then a restart so the
// importer, qBittorrent and the disk guard pick the folders up.

const keySetupComplete = "setup_complete"

// libraryDirSettings pairs each library folder setting with its config field.
func libraryDirSettings(cfg *config.Config) []struct {
	key   string
	field *string
	name  string
} {
	return []struct {
		key   string
		field *string
		name  string
	}{
		{keyLibMovies, &cfg.MoviesDir, "movies"},
		{keyLibTV, &cfg.TVDir, "tv"},
		{keyLibEbooks, &cfg.EbooksDir, "ebooks"},
		{keyLibAudiobooks, &cfg.AudiobooksDir, "audiobooks"},
		{keyLibMusic, &cfg.MusicDir, "music"},
		{keyLibDownloads, &cfg.DownloadsDir, "downloads"},
	}
}

// ApplySavedLibraryDirs makes folders chosen in the app the ones the whole app uses.
// They used to steer only the library scans, while imports, qBittorrent's save path and
// the disk guard kept the startup environment's folders — so picking a folder in the
// app changed where scans looked but not where downloads landed. Call once at startup,
// before anything reads the library folders.
func ApplySavedLibraryDirs(ctx context.Context, get func(ctx context.Context, key, def string) string, cfg *config.Config, log *slog.Logger) {
	for _, d := range libraryDirSettings(cfg) {
		v := strings.TrimSpace(get(ctx, d.key, ""))
		if v == "" || v == *d.field {
			continue
		}
		if log != nil {
			log.Info("library folder: using the folder chosen in the app", "library", d.name, "folder", v, "environment_default", *d.field)
		}
		*d.field = v
	}
	// A folder in or above the data dir (an older save, or the environment) still runs —
	// refusing to start would be worse — but it's said loudly, here and on the health panel.
	if log != nil {
		for _, d := range libraryDirSettings(cfg) {
			if *d.field != "" && libroots.UnderDataDir(*d.field, cfg.DataDir) {
				log.Error("library folder is inside or contains Arrmada's data folder; move it to its own mount",
					"library", d.name, "folder", *d.field, "data_dir", cfg.DataDir)
			}
		}
	}
}

// inContainer reports whether Arrmada runs in Docker, where exiting restarts it
// (restart: unless-stopped). Outside a container a restart would just stop the app.
func inContainer() bool {
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	return os.Getenv("ARRMADA_IN_CONTAINER") == "1"
}

// candidateMounts are the folders a container install can see media under.
var candidateMounts = []string{"/storage", "/media", "/transcode"}

// folderNames are the usual names for each library, most likely first.
var folderNames = map[string][]string{
	"movies":     {"movies", "films", "movie"},
	"tv":         {"tvshows", "tv", "tv shows", "shows", "series", "television"},
	"ebooks":     {"ebooks", "e-books", "books"},
	"audiobooks": {"audiobooks", "audio books", "audiobook"},
	"music":      {"music"},
	"downloads":  {"torrents", "downloads", "download"},
}

// suggestFolders looks two levels into each mount for folders named like each library,
// so the wizard can pre-fill "/storage/media/movies" instead of an empty box.
func suggestFolders(mounts []string) map[string]string {
	type dir struct {
		path  string
		name  string
		depth int
	}
	var dirs []dir
	for _, m := range mounts {
		if m == "/transcode" {
			continue
		}
		top, err := os.ReadDir(m)
		if err != nil {
			continue
		}
		for _, e := range top {
			if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			p := filepath.ToSlash(filepath.Join(m, e.Name()))
			dirs = append(dirs, dir{p, strings.ToLower(e.Name()), 1})
			sub, err := os.ReadDir(p)
			if err != nil {
				continue
			}
			for _, s := range sub {
				if s.IsDir() && !strings.HasPrefix(s.Name(), ".") {
					dirs = append(dirs, dir{filepath.ToSlash(filepath.Join(p, s.Name())), strings.ToLower(s.Name()), 2})
				}
			}
		}
	}
	out := map[string]string{}
	for lib, names := range folderNames {
		best, bestRank := "", 1<<30
		for _, d := range dirs {
			for i, n := range names {
				if d.name != n {
					continue
				}
				if rank := i*10 + d.depth; rank < bestRank {
					best, bestRank = d.path, rank
				}
			}
		}
		if best != "" {
			out[lib] = best
		}
	}
	return out
}

// librariesChosen reports whether anyone has pointed the libraries somewhere: in the
// app, or with the per-library environment variables an older installer wrote.
func (a *api) librariesChosen(ctx context.Context) bool {
	for _, k := range []string{keyLibMovies, keyLibTV} {
		if strings.TrimSpace(a.deps.Settings.Get(ctx, k, "")) != "" {
			return true
		}
	}
	for _, e := range []string{"ARRMADA_MOVIES_DIR", "ARRMADA_TV_DIR"} {
		if strings.TrimSpace(os.Getenv(e)) != "" {
			return true
		}
	}
	return false
}

// handleSetupState — GET /api/v1/setup
func (a *api) handleSetupState(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	tmdb := a.deps.APIKeys != nil && a.deps.APIKeys.Value(ctx, "tmdb") != ""
	chosen := a.librariesChosen(ctx)
	complete := a.deps.Settings.GetBool(ctx, keySetupComplete, false)

	saved := map[string]string{
		"movies": a.libMovies(r), "tv": a.libTV(r), "ebooks": a.libEbooks(r),
		"audiobooks": a.libAudiobooks(r), "music": a.libMusic(r), "downloads": a.libDownloads(r),
	}
	c := a.deps.Config
	running := map[string]string{
		"movies": c.MoviesDir, "tv": c.TVDir, "ebooks": c.EbooksDir,
		"audiobooks": c.AudiobooksDir, "music": c.MusicDir, "downloads": c.DownloadsDir,
	}
	restartNeeded := false
	for k, v := range saved {
		if v != running[k] {
			restartNeeded = true
		}
	}
	mounts := []string{}
	for _, m := range candidateMounts {
		if fi, err := os.Stat(m); err == nil && fi.IsDir() {
			mounts = append(mounts, m)
		}
	}
	var keys any = []any{}
	if a.deps.APIKeys != nil {
		keys = a.deps.APIKeys.Status(ctx)
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"needed":           !complete && (!tmdb || !chosen),
		"complete":         complete,
		"tmdb_configured":  tmdb,
		"libraries_chosen": chosen,
		"keys":             keys,
		"library":          saved,
		"running":          running,
		"restart_needed":   restartNeeded,
		"can_restart":      a.deps.Restart != nil && inContainer(),
		"mounts":           mounts,
		"suggestions":      suggestFolders(mounts),
	})
}

// handleSetupComplete — POST /api/v1/setup/complete (finished or skipped)
func (a *api) handleSetupComplete(w http.ResponseWriter, r *http.Request) {
	if err := a.deps.Settings.Set(r.Context(), keySetupComplete, "true"); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not save")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "ok"})
}

// handleRestart — POST /api/v1/system/restart. Shuts down cleanly; Docker's restart
// policy brings the container straight back with the saved settings applied.
func (a *api) handleRestart(w http.ResponseWriter, r *http.Request) {
	if a.deps.Restart == nil || !inContainer() {
		a.writeError(w, http.StatusBadRequest, "Arrmada can only restart itself inside Docker — restart it the way you started it")
		return
	}
	a.deps.Log.Info("restart requested from the app")
	a.writeJSON(w, http.StatusAccepted, map[string]any{"status": "restarting"})
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
	a.deps.Restart()
}
