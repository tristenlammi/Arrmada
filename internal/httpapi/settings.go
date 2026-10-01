package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/recyclebin"
)

// serverZone names the timezone the server evaluates schedules in, so the UI can say
// which clock the encode window is measured against. Falls back to the UTC offset when
// the zone is unnamed (a bare TZ=UTC, or a container with no tzdata).
func serverZone() string {
	name, offset := time.Now().Zone()
	if name != "" && name != "UTC" {
		return name
	}
	if offset == 0 {
		return "UTC"
	}
	return time.Now().Format("-07:00")
}

const (
	keySearchOnAdd         = "search_on_add"
	keyNamingFolder        = "naming_movie_folder"
	keyNamingFile          = "naming_movie_file"
	keyNamingSeriesFolder  = "naming_series_folder"
	keyNamingSeriesSeason  = "naming_series_season"
	keyNamingSeriesEpisode = "naming_series_episode"
	keyWriteNFO            = "write_nfo"
	keyDownloadArtwrk      = "download_artwork"
	keyBooksEnabled        = "module_books_enabled"
	keyMusicEnabled        = "module_music_enabled"
	keyDiskGuard           = download.KeyDiskGuard
	keyDiskGuardPause      = download.KeyDiskGuardPause
	keyDiskGuardResume     = download.KeyDiskGuardResum
)

// booksEnabled reports whether the Books module is turned on (default true). Used to gate
// the nav entry + Discover tab; disabling hides Books without deleting any data.
func (a *api) booksEnabled(ctx context.Context) bool {
	return a.deps.Settings.GetBool(ctx, keyBooksEnabled, true)
}

// musicEnabled reports whether the Music module is turned on (default true). Gates the nav
// entry (the module itself is still on the roadmap).
func (a *api) musicEnabled(ctx context.Context) bool {
	return a.deps.Settings.GetBool(ctx, keyMusicEnabled, true)
}

// handleGetSettings returns the user-facing app preferences.
func (a *api) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	a.writeJSON(w, http.StatusOK, map[string]any{
		"search_on_add":           a.deps.Settings.GetBool(ctx, keySearchOnAdd, true),
		"naming_movie_folder":     a.deps.Settings.Get(ctx, keyNamingFolder, library.DefaultMovieFolder),
		"naming_movie_file":       a.deps.Settings.Get(ctx, keyNamingFile, library.DefaultMovieFile),
		"naming_series_folder":    a.deps.Settings.Get(ctx, keyNamingSeriesFolder, library.DefaultSeriesFolder),
		"naming_series_season":    a.deps.Settings.Get(ctx, keyNamingSeriesSeason, library.DefaultSeasonFolder),
		"naming_series_episode":   a.deps.Settings.Get(ctx, keyNamingSeriesEpisode, library.DefaultEpisodeFile),
		"write_nfo":               a.deps.Settings.GetBool(ctx, keyWriteNFO, false),
		"download_artwork":        a.deps.Settings.GetBool(ctx, keyDownloadArtwrk, false),
		"books_enabled":           a.booksEnabled(ctx),
		"music_enabled":           a.musicEnabled(ctx),
		"plex_login_enabled":      a.deps.Settings.GetBool(ctx, "plex_login_enabled", false),
		"tmdb_region":             a.deps.Settings.Get(ctx, "tmdb_region", ""),
		"plex_login_auto_approve": a.deps.Settings.GetBool(ctx, "plex_login_auto_approve", true),
		// The server's own clock and zone, for anything scheduled (Convert has its own
		// settings endpoint, which reports the same).
		"server_time": time.Now().Format("15:04"),
		"server_tz":   serverZone(),
		// Recycle bin guard rails. These default to REAL limits, not 0/unlimited: the
		// bin is on by default and every delete, quality upgrade and Convert original
		// lands in it, so an unlimited default silently grows until the volume fills.
		// Set either to 0 to opt back into unlimited.
		"recycle_max_gb":         a.deps.Settings.Get(ctx, "recycle_max_gb", recyclebin.DefaultMaxGB),
		"recycle_retention_days": a.deps.Settings.Get(ctx, "recycle_retention_days", recyclebin.DefaultRetentionDays),
		// Downloads disk guard. On by default — a full downloads volume errors every
		// torrent at once and, on a shared cache pool, takes everything else with it.
		"downloads_disk_guard":            a.deps.Settings.GetBool(ctx, keyDiskGuard, download.DefaultDiskGuard),
		"downloads_disk_guard_pause_pct":  a.deps.Settings.Get(ctx, keyDiskGuardPause, strconv.Itoa(download.DefaultDiskGuardPause)),
		"downloads_disk_guard_resume_pct": a.deps.Settings.Get(ctx, keyDiskGuardResume, strconv.Itoa(download.DefaultDiskGuardResum)),
	})
}

// handleUpdateSettings persists changed preferences (only provided keys change).
func (a *api) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SearchOnAdd          *bool   `json:"search_on_add"`
		NamingMovieFolder    *string `json:"naming_movie_folder"`
		NamingMovieFile      *string `json:"naming_movie_file"`
		NamingSeriesFolder   *string `json:"naming_series_folder"`
		NamingSeriesSeason   *string `json:"naming_series_season"`
		NamingSeriesEpisode  *string `json:"naming_series_episode"`
		WriteNFO             *bool   `json:"write_nfo"`
		DownloadArtwork      *bool   `json:"download_artwork"`
		BooksEnabled         *bool   `json:"books_enabled"`
		MusicEnabled         *bool   `json:"music_enabled"`
		PlexLoginEnabled     *bool   `json:"plex_login_enabled"`
		TMDBRegion           *string `json:"tmdb_region"`
		PlexLoginAutoApprove *bool   `json:"plex_login_auto_approve"`
		RecycleMaxGB         *string `json:"recycle_max_gb"`
		RecycleRetentionDays *string `json:"recycle_retention_days"`
		DiskGuard            *bool   `json:"downloads_disk_guard"`
		DiskGuardPausePct    *string `json:"downloads_disk_guard_pause_pct"`
		DiskGuardResumePct   *string `json:"downloads_disk_guard_resume_pct"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	save := func(err error) bool {
		if err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not save settings")
			return false
		}
		return true
	}

	// The disk guard's two thresholds are validated together: a resume point at or
	// above the pause point pauses and resumes on alternate passes forever. The guard
	// corrects that defensively at read time, but rejecting it here means the UI says
	// so rather than silently storing a number it won't honour.
	if req.DiskGuardPausePct != nil || req.DiskGuardResumePct != nil {
		pausePct := a.pctSetting(ctx, keyDiskGuardPause, download.DefaultDiskGuardPause, req.DiskGuardPausePct)
		resumePct := a.pctSetting(ctx, keyDiskGuardResume, download.DefaultDiskGuardResum, req.DiskGuardResumePct)
		if pausePct < 1 || pausePct > 99 {
			a.writeError(w, http.StatusBadRequest, "pause percentage must be between 1 and 99")
			return
		}
		if resumePct < 0 || resumePct >= pausePct {
			a.writeError(w, http.StatusBadRequest, "resume percentage must be below the pause percentage")
			return
		}
	}

	if req.SearchOnAdd != nil && !save(a.deps.Settings.SetBool(ctx, keySearchOnAdd, *req.SearchOnAdd)) {
		return
	}
	if req.NamingMovieFolder != nil && !save(a.deps.Settings.Set(ctx, keyNamingFolder, *req.NamingMovieFolder)) {
		return
	}
	if req.NamingMovieFile != nil && !save(a.deps.Settings.Set(ctx, keyNamingFile, *req.NamingMovieFile)) {
		return
	}
	if req.NamingSeriesFolder != nil && !save(a.deps.Settings.Set(ctx, keyNamingSeriesFolder, *req.NamingSeriesFolder)) {
		return
	}
	if req.NamingSeriesSeason != nil && !save(a.deps.Settings.Set(ctx, keyNamingSeriesSeason, *req.NamingSeriesSeason)) {
		return
	}
	if req.NamingSeriesEpisode != nil && !save(a.deps.Settings.Set(ctx, keyNamingSeriesEpisode, *req.NamingSeriesEpisode)) {
		return
	}
	if req.WriteNFO != nil && !save(a.deps.Settings.SetBool(ctx, keyWriteNFO, *req.WriteNFO)) {
		return
	}
	if req.DownloadArtwork != nil && !save(a.deps.Settings.SetBool(ctx, keyDownloadArtwrk, *req.DownloadArtwork)) {
		return
	}
	if req.BooksEnabled != nil && !save(a.deps.Settings.SetBool(ctx, keyBooksEnabled, *req.BooksEnabled)) {
		return
	}
	if req.PlexLoginEnabled != nil && !save(a.deps.Settings.SetBool(ctx, "plex_login_enabled", *req.PlexLoginEnabled)) {
		return
	}
	if req.TMDBRegion != nil {
		// ISO 3166-1 alpha-2 ("AU"), or empty to go back to TMDB's global lists.
		region := strings.ToUpper(strings.TrimSpace(*req.TMDBRegion))
		if region != "" && (len(region) != 2 || region[0] < 'A' || region[0] > 'Z' || region[1] < 'A' || region[1] > 'Z') {
			a.writeError(w, http.StatusBadRequest, "region must be a two-letter country code, e.g. AU")
			return
		}
		if !save(a.deps.Settings.Set(ctx, "tmdb_region", region)) {
			return
		}
	}
	if req.PlexLoginAutoApprove != nil && !save(a.deps.Settings.SetBool(ctx, "plex_login_auto_approve", *req.PlexLoginAutoApprove)) {
		return
	}
	if req.RecycleMaxGB != nil && !save(a.deps.Settings.Set(ctx, "recycle_max_gb", strings.TrimSpace(*req.RecycleMaxGB))) {
		return
	}
	if req.RecycleRetentionDays != nil && !save(a.deps.Settings.Set(ctx, "recycle_retention_days", strings.TrimSpace(*req.RecycleRetentionDays))) {
		return
	}
	if req.MusicEnabled != nil && !save(a.deps.Settings.SetBool(ctx, keyMusicEnabled, *req.MusicEnabled)) {
		return
	}
	if req.DiskGuard != nil && !save(a.deps.Settings.SetBool(ctx, keyDiskGuard, *req.DiskGuard)) {
		return
	}
	if req.DiskGuardPausePct != nil && !save(a.deps.Settings.Set(ctx, keyDiskGuardPause, strings.TrimSpace(*req.DiskGuardPausePct))) {
		return
	}
	if req.DiskGuardResumePct != nil && !save(a.deps.Settings.Set(ctx, keyDiskGuardResume, strings.TrimSpace(*req.DiskGuardResumePct))) {
		return
	}
	a.handleGetSettings(w, r)
}

// validHHMM reports whether v is a 24-hour "HH:MM" time.
func validHHMM(v string) bool {
	p := strings.SplitN(v, ":", 2)
	if len(p) != 2 {
		return false
	}
	h, err1 := strconv.Atoi(p[0])
	m, err2 := strconv.Atoi(p[1])
	return err1 == nil && err2 == nil && h >= 0 && h < 24 && m >= 0 && m < 60
}

// pctSetting resolves a percentage that may be arriving in this request or may already
// be stored, so the two disk-guard thresholds can be validated against each other even
// when only one of them is being changed.
func (a *api) pctSetting(ctx context.Context, key string, def int, incoming *string) int {
	raw := a.deps.Settings.Get(ctx, key, strconv.Itoa(def))
	if incoming != nil {
		raw = *incoming
	}
	n, err := strconv.Atoi(strings.TrimSpace(raw))
	if err != nil {
		return def
	}
	return n
}
