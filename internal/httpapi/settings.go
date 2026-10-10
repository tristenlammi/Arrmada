package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/library"
	"github.com/tristenlammi/arrmada/internal/quality"
	"github.com/tristenlammi/arrmada/internal/recyclebin"
	"github.com/tristenlammi/arrmada/internal/requests"
	"github.com/tristenlammi/arrmada/internal/series"
	"github.com/tristenlammi/arrmada/internal/settings"
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
	keyDiskGuard           = download.KeyDiskGuard
	keyDiskGuardPause      = download.KeyDiskGuardPause
	keyDiskGuardResume     = download.KeyDiskGuardResum
)

// booksEnabled reports whether the Books module is turned on (default true). Used to gate
// the nav entry + Discover tab; disabling hides Books without deleting any data.
func (a *api) booksEnabled(ctx context.Context) bool {
	return a.deps.Settings.GetBool(ctx, settings.KeyModuleBooks, true)
}

// musicEnabled reports whether the Music module is turned on (off by default — it's a
// preview). Off hides the nav entry, 404s the Music API and stops its searches and imports;
// nothing is deleted.
func (a *api) musicEnabled(ctx context.Context) bool {
	return a.deps.Settings.GetBool(ctx, settings.KeyModuleMusic, settings.ModuleMusicDefault)
}

// handleGetSettings returns the user-facing app preferences. Every key here must also be
// accepted by handleUpdateSettings: clients PUT these values back, and the decoder
// rejects fields it doesn't know. A read-only server_time/server_tz in this response
// broke every Settings save for weeks, so read-only facts go on their own endpoint
// (Convert reports the server clock from /convert/settings).
// seriesMonitorDefault is the monitoring preset a new show gets when the add doesn't pick
// one; a stored value that isn't a preset reads as the default.
func (a *api) seriesMonitorDefault(ctx context.Context) string {
	if p := a.deps.Settings.Get(ctx, series.KeyMonitorDefault, series.DefaultMonitorPreset); series.ValidPreset(p) {
		return p
	}
	return series.DefaultMonitorPreset
}

func (a *api) handleGetSettings(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	quota := requests.GlobalLimits(func(key string) string { return a.deps.Settings.Get(ctx, key, "") })
	a.writeJSON(w, http.StatusOK, map[string]any{
		"search_on_add":           a.deps.Settings.GetBool(ctx, keySearchOnAdd, true),
		"series_monitor_default":  a.seriesMonitorDefault(ctx),
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
		"plex_signin_staff":       a.deps.Settings.GetBool(ctx, keyPlexSignInStaff, false),
		"tmdb_region":             a.deps.Settings.Get(ctx, "tmdb_region", ""),
		"plex_login_auto_approve": a.plexAutoApproval(ctx).All(),
		// Which media types a new Plex sign-in auto-approves ("movie,series,book").
		"plex_login_auto_approve_types": a.plexAutoApproval(ctx).String(),
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
		// Stall fail-over, in minutes with no progress (0 = never). On by default: a
		// stalled download is only removed once another release has been grabbed.
		"downloads_stall_minutes": automation.ParseStallMinutes(a.deps.Settings.Get(ctx, automation.KeyStallMinutes, "")),
		// How many upgrades one upgrade sweep may grab (0 = no limit); the rest wait for
		// the next sweep, so a profile edit can't queue the whole library at once.
		"upgrade_max_grabs_per_sweep": automation.ParseUpgradeBudget(a.deps.Settings.Get(ctx, automation.KeyUpgradeBudget, "")),
		// Request limits per person, per window of days (0 = unlimited; Settings → Users).
		"request_quota_days": quota.Days, "request_quota_movies": quota.Movie,
		"request_quota_seasons": quota.Season, "request_quota_books": quota.Book,
	})
}

// maxUpgradeBudget bounds the per-sweep upgrade limit. Anything near it is effectively
// "no limit", which 0 already says.
const maxUpgradeBudget = 1000

// settingsUpdate is a PUT /settings body: a nil field is left as it is.
type settingsUpdate struct {
	SearchOnAdd          *bool   `json:"search_on_add"`
	SeriesMonitorDefault *string `json:"series_monitor_default"`
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
	PlexSignInStaff      *bool   `json:"plex_signin_staff"`
	TMDBRegion           *string `json:"tmdb_region"`
	PlexLoginAutoApprove *bool   `json:"plex_login_auto_approve"` // legacy: every type or none
	// PlexLoginAutoApproveTypes wins over the legacy bool when both are sent.
	PlexLoginAutoApproveTypes *string `json:"plex_login_auto_approve_types"`
	RecycleMaxGB              *string `json:"recycle_max_gb"`
	RecycleRetentionDays      *string `json:"recycle_retention_days"`
	DiskGuard                 *bool   `json:"downloads_disk_guard"`
	DiskGuardPausePct         *string `json:"downloads_disk_guard_pause_pct"`
	DiskGuardResumePct        *string `json:"downloads_disk_guard_resume_pct"`
	StallMinutes              *int    `json:"downloads_stall_minutes"`
	UpgradeBudget             *int    `json:"upgrade_max_grabs_per_sweep"`
	QuotaDays                 *int    `json:"request_quota_days"`
	QuotaMovies               *int    `json:"request_quota_movies"`
	QuotaSeasons              *int    `json:"request_quota_seasons"`
	QuotaBooks                *int    `json:"request_quota_books"`
}

// adminSettingChange names the first admin-only setting req would change, or "" if it
// changes none. Module switches, who may sign in with Plex, the Discovery region and the
// bin and disk guard limits are the owner's calls; a manager runs the media day to day
// (naming, metadata files, search on add, stall fail-over).
//
// A field only counts when it differs from what's in effect now, defaults included (the
// same Get/GetBool defaults handleGetSettings answers with), so a client that sends the
// whole settings object back with those fields untouched still saves.
func (a *api) adminSettingChange(ctx context.Context, req *settingsUpdate) string {
	st := a.deps.Settings
	boolChanged := func(p *bool, cur bool) bool { return p != nil && *p != cur }
	strChanged := func(p *string, cur string) bool {
		return p != nil && strings.TrimSpace(*p) != strings.TrimSpace(cur)
	}
	switch {
	case boolChanged(req.BooksEnabled, a.booksEnabled(ctx)):
		return "the Books module"
	case boolChanged(req.MusicEnabled, a.musicEnabled(ctx)):
		return "the Music module"
	case boolChanged(req.PlexLoginEnabled, st.GetBool(ctx, "plex_login_enabled", false)):
		return "Plex sign-in"
	case boolChanged(req.PlexSignInStaff, st.GetBool(ctx, keyPlexSignInStaff, false)):
		return "whether staff may sign in with Plex"
	case req.PlexLoginAutoApproveTypes != nil && auth.ParseAutoApproval(*req.PlexLoginAutoApproveTypes) != a.plexAutoApproval(ctx),
		req.PlexLoginAutoApproveTypes == nil && req.PlexLoginAutoApprove != nil && auth.AllTypes(*req.PlexLoginAutoApprove) != a.plexAutoApproval(ctx):
		return "auto-approval of Plex sign-ins' requests"
	case req.TMDBRegion != nil && !strings.EqualFold(strings.TrimSpace(*req.TMDBRegion), strings.TrimSpace(st.Get(ctx, "tmdb_region", ""))):
		return "the Discovery region"
	case strChanged(req.RecycleMaxGB, st.Get(ctx, "recycle_max_gb", recyclebin.DefaultMaxGB)):
		return "the recycle bin size limit"
	case strChanged(req.RecycleRetentionDays, st.Get(ctx, "recycle_retention_days", recyclebin.DefaultRetentionDays)):
		return "how long the recycle bin keeps files"
	case boolChanged(req.DiskGuard, st.GetBool(ctx, keyDiskGuard, download.DefaultDiskGuard)):
		return "the downloads disk guard"
	case strChanged(req.DiskGuardPausePct, st.Get(ctx, keyDiskGuardPause, strconv.Itoa(download.DefaultDiskGuardPause))),
		strChanged(req.DiskGuardResumePct, st.Get(ctx, keyDiskGuardResume, strconv.Itoa(download.DefaultDiskGuardResum))):
		return "the disk guard's thresholds"
	case quotaChanged(req, requests.GlobalLimits(func(key string) string { return st.Get(ctx, key, "") })):
		return "the request limits"
	}
	return ""
}

// handleUpdateSettings persists changed preferences (only provided keys change).
func (a *api) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	var req settingsUpdate
	if !a.decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()

	// Checked before anything is written, so a body with one forbidden change saves
	// nothing at all rather than half of itself.
	if u, ok := userFrom(r); !ok || u == nil || !u.Role.AtLeast(auth.RoleAdmin) {
		if label := a.adminSettingChange(ctx, &req); label != "" {
			a.writeError(w, http.StatusForbidden, "Only an admin can change "+label)
			return
		}
	}
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

	if req.StallMinutes != nil && (*req.StallMinutes < 0 || *req.StallMinutes > quality.StallMaxMinutes) {
		a.writeError(w, http.StatusBadRequest, "the stall timeout must be between 0 (never) and 10080 minutes (a week)")
		return
	}
	if req.UpgradeBudget != nil && (*req.UpgradeBudget < 0 || *req.UpgradeBudget > maxUpgradeBudget) {
		a.writeError(w, http.StatusBadRequest, "upgrades per sweep must be between 0 (no limit) and "+strconv.Itoa(maxUpgradeBudget))
		return
	}
	if msg := validQuota(req.QuotaDays, req.QuotaMovies, req.QuotaSeasons, req.QuotaBooks); msg != "" {
		a.writeError(w, http.StatusBadRequest, msg)
		return
	}
	// ISO 3166-1 alpha-2 ("AU"), or empty to go back to TMDB's global lists. Checked up
	// here with the rest, so a bad region doesn't leave the keys before it saved.
	var region string
	if req.TMDBRegion != nil {
		region = strings.ToUpper(strings.TrimSpace(*req.TMDBRegion))
		if region != "" && (len(region) != 2 || region[0] < 'A' || region[0] > 'Z' || region[1] < 'A' || region[1] > 'Z') {
			a.writeError(w, http.StatusBadRequest, "region must be a two-letter country code, e.g. AU")
			return
		}
	}

	if req.SearchOnAdd != nil && !save(a.deps.Settings.SetBool(ctx, keySearchOnAdd, *req.SearchOnAdd)) {
		return
	}
	for key, v := range map[string]*int{
		requests.KeyQuotaDays: req.QuotaDays, requests.KeyQuotaMovies: req.QuotaMovies,
		requests.KeyQuotaSeasons: req.QuotaSeasons, requests.KeyQuotaBooks: req.QuotaBooks,
	} {
		if v != nil && !save(a.deps.Settings.Set(ctx, key, strconv.Itoa(*v))) {
			return
		}
	}
	if req.SeriesMonitorDefault != nil {
		if !series.ValidPreset(*req.SeriesMonitorDefault) {
			a.writeError(w, http.StatusBadRequest, "unknown monitoring preset "+strconv.Quote(*req.SeriesMonitorDefault))
			return
		}
		if !save(a.deps.Settings.Set(ctx, series.KeyMonitorDefault, *req.SeriesMonitorDefault)) {
			return
		}
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
	if req.BooksEnabled != nil && !save(a.deps.Settings.SetBool(ctx, settings.KeyModuleBooks, *req.BooksEnabled)) {
		return
	}
	if req.PlexLoginEnabled != nil && !save(a.deps.Settings.SetBool(ctx, "plex_login_enabled", *req.PlexLoginEnabled)) {
		return
	}
	if req.PlexSignInStaff != nil && !save(a.deps.Settings.SetBool(ctx, keyPlexSignInStaff, *req.PlexSignInStaff)) {
		return
	}
	if req.TMDBRegion != nil && !save(a.deps.Settings.Set(ctx, "tmdb_region", region)) {
		return
	}
	if types, ok := plexAutoApproveUpdate(&req); ok && !save(a.deps.Settings.Set(ctx, keyPlexAutoApproveTypes, types)) {
		return
	}
	// The page sends every setting on each save, so note whether the bin's rules really changed.
	binWas := a.deps.Settings.Get(ctx, "recycle_max_gb", recyclebin.DefaultMaxGB) + "/" +
		a.deps.Settings.Get(ctx, "recycle_retention_days", recyclebin.DefaultRetentionDays)
	if req.RecycleMaxGB != nil && !save(a.deps.Settings.Set(ctx, "recycle_max_gb", strings.TrimSpace(*req.RecycleMaxGB))) {
		return
	}
	if req.RecycleRetentionDays != nil && !save(a.deps.Settings.Set(ctx, "recycle_retention_days", strings.TrimSpace(*req.RecycleRetentionDays))) {
		return
	}
	binNow := a.deps.Settings.Get(ctx, "recycle_max_gb", recyclebin.DefaultMaxGB) + "/" +
		a.deps.Settings.Get(ctx, "recycle_retention_days", recyclebin.DefaultRetentionDays)
	if binNow != binWas && a.deps.Convert != nil {
		// Convert holds back files whose originals didn't fit in the bin; a new cap is
		// the owner's answer to that, so they shouldn't wait a day to see it.
		a.deps.Convert.BinSettingsChanged(ctx)
	}
	if req.MusicEnabled != nil && !save(a.deps.Settings.SetBool(ctx, settings.KeyModuleMusic, *req.MusicEnabled)) {
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
	if req.StallMinutes != nil && !save(a.deps.Settings.Set(ctx, automation.KeyStallMinutes, strconv.Itoa(*req.StallMinutes))) {
		return
	}
	if req.UpgradeBudget != nil && !save(a.deps.Settings.Set(ctx, automation.KeyUpgradeBudget, strconv.Itoa(*req.UpgradeBudget))) {
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
