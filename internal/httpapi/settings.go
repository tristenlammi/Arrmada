package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/recyclebin"
)

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
		"plex_login_auto_approve": a.deps.Settings.GetBool(ctx, "plex_login_auto_approve", true),
		// Convert module.
		"convert_skip_hardlinked":  a.deps.Settings.GetBool(ctx, "convert_skip_hardlinked", true),
		"convert_keep_audio_langs": a.deps.Settings.Get(ctx, "convert_keep_audio_langs", ""),
		"convert_add_stereo":       a.deps.Settings.GetBool(ctx, "convert_add_stereo", false),
		"convert_loudnorm":         a.deps.Settings.GetBool(ctx, "convert_loudnorm", false),
		// Convert — focused model: target codec, subtitle toggle, schedule, quality safety.
		"convert_target_codec":     a.deps.Settings.Get(ctx, "convert_target_codec", "hevc"),
		"convert_auto":             a.deps.Settings.GetBool(ctx, "convert_auto", false),
		"convert_quality_gate":     a.deps.Settings.GetBool(ctx, "convert_quality_gate", true),
		"convert_min_ssim":         a.deps.Settings.Get(ctx, "convert_min_ssim", "0.95"),
		"convert_workers":          a.deps.Settings.Get(ctx, "convert_workers", "1"),
		"convert_sweep_start":      a.deps.Settings.Get(ctx, "convert_sweep_start", ""),
		"convert_scan_at":          a.deps.Settings.Get(ctx, "convert_scan_at", "03:00"),
		"convert_cpu_cores":        a.deps.Settings.Get(ctx, "convert_cpu_cores", "0"),
		"convert_cpu_above_height": a.deps.Settings.Get(ctx, "convert_cpu_above_height", "2160"),
		"convert_av1_recode_hevc":  a.deps.Settings.GetBool(ctx, "convert_av1_recode_hevc", false),
		"convert_sweep_end":        a.deps.Settings.Get(ctx, "convert_sweep_end", ""),
		"convert_max_failures":     a.deps.Settings.Get(ctx, "convert_max_failures", "3"),
		"convert_scratch_dir":      a.deps.Settings.Get(ctx, "convert_scratch_dir", ""),
		"convert_vaapi_device":     a.deps.Settings.Get(ctx, "convert_vaapi_device", ""),
		// Recycle bin guard rails. These default to REAL limits, not 0/unlimited: the
		// bin is on by default and every delete, quality upgrade and Convert original
		// lands in it, so an unlimited default silently grows until the volume fills.
		// Set either to 0 to opt back into unlimited.
		"recycle_max_gb":         a.deps.Settings.Get(ctx, "recycle_max_gb", recyclebin.DefaultMaxGB),
		"recycle_retention_days": a.deps.Settings.Get(ctx, "recycle_retention_days", recyclebin.DefaultRetentionDays),
	})
}

// handleUpdateSettings persists changed preferences (only provided keys change).
func (a *api) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SearchOnAdd           *bool   `json:"search_on_add"`
		NamingMovieFolder     *string `json:"naming_movie_folder"`
		NamingMovieFile       *string `json:"naming_movie_file"`
		NamingSeriesFolder    *string `json:"naming_series_folder"`
		NamingSeriesSeason    *string `json:"naming_series_season"`
		NamingSeriesEpisode   *string `json:"naming_series_episode"`
		WriteNFO              *bool   `json:"write_nfo"`
		DownloadArtwork       *bool   `json:"download_artwork"`
		BooksEnabled          *bool   `json:"books_enabled"`
		MusicEnabled          *bool   `json:"music_enabled"`
		PlexLoginEnabled      *bool   `json:"plex_login_enabled"`
		PlexLoginAutoApprove  *bool   `json:"plex_login_auto_approve"`
		ConvertSkipHardlinked *bool   `json:"convert_skip_hardlinked"`
		ConvertKeepAudioLangs *string `json:"convert_keep_audio_langs"`
		ConvertAddStereo      *bool   `json:"convert_add_stereo"`
		ConvertLoudnorm       *bool   `json:"convert_loudnorm"`
		ConvertTargetCodec    *string `json:"convert_target_codec"`
		ConvertAuto           *bool   `json:"convert_auto"`
		ConvertQualityGate    *bool   `json:"convert_quality_gate"`
		ConvertMinSSIM        *string `json:"convert_min_ssim"`
		ConvertWorkers        *string `json:"convert_workers"`
		ConvertSweepStart     *string `json:"convert_sweep_start"`
		ConvertScanAt         *string `json:"convert_scan_at"`
		ConvertCPUCores       *string `json:"convert_cpu_cores"`
		ConvertCPUAboveHeight *string `json:"convert_cpu_above_height"`
		ConvertAV1RecodeHEVC  *bool   `json:"convert_av1_recode_hevc"`
		ConvertSweepEnd       *string `json:"convert_sweep_end"`
		ConvertMaxFailures    *string `json:"convert_max_failures"`
		ConvertScratchDir     *string `json:"convert_scratch_dir"`
		ConvertVaapiDevice    *string `json:"convert_vaapi_device"`
		RecycleMaxGB          *string `json:"recycle_max_gb"`
		RecycleRetentionDays  *string `json:"recycle_retention_days"`
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
	if req.PlexLoginAutoApprove != nil && !save(a.deps.Settings.SetBool(ctx, "plex_login_auto_approve", *req.PlexLoginAutoApprove)) {
		return
	}
	if req.ConvertSkipHardlinked != nil && !save(a.deps.Settings.SetBool(ctx, "convert_skip_hardlinked", *req.ConvertSkipHardlinked)) {
		return
	}
	if req.ConvertKeepAudioLangs != nil && !save(a.deps.Settings.Set(ctx, "convert_keep_audio_langs", *req.ConvertKeepAudioLangs)) {
		return
	}
	if req.ConvertAddStereo != nil && !save(a.deps.Settings.SetBool(ctx, "convert_add_stereo", *req.ConvertAddStereo)) {
		return
	}
	if req.ConvertLoudnorm != nil && !save(a.deps.Settings.SetBool(ctx, "convert_loudnorm", *req.ConvertLoudnorm)) {
		return
	}
	if req.ConvertTargetCodec != nil && !save(a.deps.Settings.Set(ctx, "convert_target_codec", *req.ConvertTargetCodec)) {
		return
	}
	if req.ConvertAuto != nil && !save(a.deps.Settings.SetBool(ctx, "convert_auto", *req.ConvertAuto)) {
		return
	}
	if req.ConvertQualityGate != nil && !save(a.deps.Settings.SetBool(ctx, "convert_quality_gate", *req.ConvertQualityGate)) {
		return
	}
	if req.ConvertMinSSIM != nil && !save(a.deps.Settings.Set(ctx, "convert_min_ssim", *req.ConvertMinSSIM)) {
		return
	}
	if req.ConvertWorkers != nil && !save(a.deps.Settings.Set(ctx, "convert_workers", *req.ConvertWorkers)) {
		return
	}
	// Convert settings are validated here rather than trusted from the client: the UI's
	// min/max attributes aren't enforced for programmatic writes, and a value like
	// min_ssim=2.0 is unreachable, so every encode would be discarded with no explanation.
	if req.ConvertMinSSIM != nil {
		if v, err := strconv.ParseFloat(strings.TrimSpace(*req.ConvertMinSSIM), 64); err != nil || v <= 0 || v > 1 {
			a.writeError(w, http.StatusBadRequest, "quality gate minimum must be between 0 and 1")
			return
		}
	}
	for _, f := range []struct {
		v    *string
		name string
	}{
		{req.ConvertSweepStart, "schedule start"}, {req.ConvertSweepEnd, "schedule end"}, {req.ConvertScanAt, "scan time"},
	} {
		if f.v == nil {
			continue
		}
		if t := strings.TrimSpace(*f.v); t != "" && !validHHMM(t) {
			a.writeError(w, http.StatusBadRequest, f.name+" must be a time like 03:00")
			return
		}
	}
	if req.ConvertTargetCodec != nil {
		if c := strings.TrimSpace(*req.ConvertTargetCodec); c != "hevc" && c != "av1" {
			a.writeError(w, http.StatusBadRequest, "target codec must be hevc or av1")
			return
		}
	}
	if req.ConvertScanAt != nil && !save(a.deps.Settings.Set(ctx, "convert_scan_at", *req.ConvertScanAt)) {
		return
	}
	if req.ConvertCPUCores != nil && !save(a.deps.Settings.Set(ctx, "convert_cpu_cores", *req.ConvertCPUCores)) {
		return
	}
	if req.ConvertCPUAboveHeight != nil && !save(a.deps.Settings.Set(ctx, "convert_cpu_above_height", *req.ConvertCPUAboveHeight)) {
		return
	}
	if req.ConvertAV1RecodeHEVC != nil && !save(a.deps.Settings.SetBool(ctx, "convert_av1_recode_hevc", *req.ConvertAV1RecodeHEVC)) {
		return
	}
	if req.ConvertSweepStart != nil && !save(a.deps.Settings.Set(ctx, "convert_sweep_start", *req.ConvertSweepStart)) {
		return
	}
	if req.ConvertSweepEnd != nil && !save(a.deps.Settings.Set(ctx, "convert_sweep_end", *req.ConvertSweepEnd)) {
		return
	}
	if req.ConvertMaxFailures != nil && !save(a.deps.Settings.Set(ctx, "convert_max_failures", *req.ConvertMaxFailures)) {
		return
	}
	if req.ConvertScratchDir != nil && !save(a.deps.Settings.Set(ctx, "convert_scratch_dir", strings.TrimSpace(*req.ConvertScratchDir))) {
		return
	}
	if req.ConvertVaapiDevice != nil && !save(a.deps.Settings.Set(ctx, "convert_vaapi_device", strings.TrimSpace(*req.ConvertVaapiDevice))) {
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
