package httpapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/automation"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/indexer"
	"github.com/tristenlammi/arrmada/internal/kepub"
	"github.com/tristenlammi/arrmada/internal/langs"
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
	// Global seeding settings (Settings → Downloads)
	keyGlobalSeedEnabled        = automation.KeyGlobalSeedEnabled
	keyGlobalSeedRatioLimit     = automation.KeyGlobalSeedRatioLimit
	keyGlobalMaxSeedingTime     = automation.KeyGlobalMaxSeedingTime
	keyGlobalSeedingLimitAction = automation.KeyGlobalSeedingLimitAction
	keyGlobalApplyToExisting    = automation.KeyGlobalApplyToExisting
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
	out := map[string]any{
		"search_on_add":          a.deps.Settings.GetBool(ctx, keySearchOnAdd, true),
		"series_monitor_default": a.seriesMonitorDefault(ctx),
		"naming_movie_folder":    a.deps.Settings.Get(ctx, keyNamingFolder, library.DefaultMovieFolder),
		"naming_movie_file":      a.deps.Settings.Get(ctx, keyNamingFile, library.DefaultMovieFile),
		"naming_series_folder":   a.deps.Settings.Get(ctx, keyNamingSeriesFolder, library.DefaultSeriesFolder),
		"naming_series_season":   a.deps.Settings.Get(ctx, keyNamingSeriesSeason, library.DefaultSeasonFolder),
		"naming_series_episode":  a.deps.Settings.Get(ctx, keyNamingSeriesEpisode, library.DefaultEpisodeFile),
		// A new show folder keeps {year} only when another show shares its title (off = always).
		"naming_series_year_when_needed": a.deps.Settings.GetBool(ctx, settings.KeyNamingSeriesYearWhenNeeded, false),
		"write_nfo":                      a.deps.Settings.GetBool(ctx, keyWriteNFO, false),
		"download_artwork":               a.deps.Settings.GetBool(ctx, keyDownloadArtwrk, false),
		"books_enabled":                  a.booksEnabled(ctx),
		"music_enabled":                  a.musicEnabled(ctx),
		"plex_login_enabled":             a.deps.Settings.GetBool(ctx, "plex_login_enabled", false),
		"plex_signin_staff":              a.deps.Settings.GetBool(ctx, keyPlexSignInStaff, false),
		"tmdb_region":                    a.deps.Settings.Get(ctx, "tmdb_region", ""),
		"plex_login_auto_approve":        a.plexAutoApproval(ctx).All(),
		// Which media types a new Plex sign-in auto-approves ("movie,series,book").
		"plex_login_auto_approve_types": a.plexAutoApproval(ctx).String(),
		// Sign in with Jellyfin (off until the owner turns it on), its Quick Connect (on with
		// it), whether it may open a staff account, and what a new sign-in auto-approves.
		"jellyfin_login_enabled":            a.deps.Settings.GetBool(ctx, keyJellyfinLoginEnabled, false),
		"jellyfin_quickconnect_enabled":     a.jellyfinQuickConnectOn(ctx),
		"jellyfin_signin_staff":             a.deps.Settings.GetBool(ctx, keyJellyfinSignInStaff, false),
		"jellyfin_login_auto_approve_types": a.jellyfinAutoApproval(ctx).String(),
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
		// "Measure a different drive": a path whose drive the guard reads instead of the
		// downloads folder's ("" = the downloads folder). For Unraid's cache pool.
		"downloads_disk_guard_measure_path": a.deps.Settings.Get(ctx, download.KeyDiskGuardMeasure, ""),
		// "Also pause when free space drops below N GB" ("0" = off): for a ZFS pool, whose
		// percentage counts only part of it.
		"downloads_disk_guard_pause_free_gb": a.deps.Settings.Get(ctx, download.KeyDiskGuardPauseFree, "0"),
		// Stall fail-over, in minutes with no progress (0 = never). On by default: a
		// stalled download is only removed once another release has been grabbed.
		"downloads_stall_minutes": automation.ParseStallMinutes(a.deps.Settings.Get(ctx, automation.KeyStallMinutes, "")),
		// How many upgrades one upgrade sweep may grab (0 = no limit); the rest wait for
		// the next sweep, so a profile edit can't queue the whole library at once.
		"upgrade_max_grabs_per_sweep": automation.ParseUpgradeBudget(a.deps.Settings.Get(ctx, automation.KeyUpgradeBudget, "")),
		// Global seeding limits: defaults for all downloads, with per-indexer overrides.
		"seeding_global_enabled":              a.deps.Settings.GetBool(ctx, automation.KeyGlobalSeedEnabled, automation.DefaultGlobalSeedEnabled),
		"seeding_global_ratio_limit":          automation.ParseGlobalSeedRatioLimit(a.deps.Settings.Get(ctx, automation.KeyGlobalSeedRatioLimit, "")),
		"seeding_global_max_time_hours":       automation.ParseGlobalMaxSeedingTime(a.deps.Settings.Get(ctx, automation.KeyGlobalMaxSeedingTime, "")),
		"seeding_global_limit_action":         string(automation.ParseGlobalSeedingLimitAction(a.deps.Settings.Get(ctx, automation.KeyGlobalSeedingLimitAction, ""))),
		"seeding_global_apply_to_existing":    automation.ParseGlobalApplyToExisting(a.deps.Settings.Get(ctx, automation.KeyGlobalApplyToExisting, "")),
		// Request limits per person, per window of days (0 = unlimited; Settings → Users).
		"request_quota_days": quota.Days, "request_quota_movies": quota.Movie,
		"request_quota_seasons": quota.Season, "request_quota_books": quota.Book,
		// Search indexers that name no categories of their own in the media type's usual
		// ones (movies 2000, TV 5000, books 7000/7020, audiobooks 3030, music 3000).
		"indexer_default_categories": a.deps.Settings.GetBool(ctx, indexer.KeyDefaultCategories, true),
		// The languages automatic book grabs accept, canonical codes in order; [] = any.
		"books_languages": a.bookLanguages(ctx),
		// The extra languages whose translated titles scans and downloads Arrmada didn't
		// grab may be named in (canonical codes); [] = English and original titles only.
		"match_title_languages": settings.ParseTitleLanguages(a.deps.Settings.Get(ctx, settings.KeyTitleLanguages, "")),
		// How releases are ranked: v2 (ordered keys, the default) or v1 (classic, the
		// rollback). Whichever decides, the other runs in the shadow and the disagreements
		// are recorded for review.
		"quality_ranking": a.qualityRanking(ctx),
		// When a release is on both Usenet and torrents: best (the better release wins,
		// the default), usenet or torrent (preferred between releases that score the same).
		"download_network_preference": quality.NormalizeNetwork(a.deps.Settings.Get(ctx, quality.KeyNetworkPreference, quality.NetworkBest)),
		// The address people reach Arrmada at from outside (https://arrmada.example.com):
		// alerts link back with it and the calendar feed's link is built from it.
		"app_public_url": a.deps.Settings.PublicURL(ctx),
		// Require two-factor for admins: an admin signing in with a password who hasn't
		// set it up must do so before any session is made (twofactor.go).
		"two_factor_admins": a.deps.Settings.GetBool(ctx, keyTwoFactorAdmins, false),
		// Convert EPUB to KEPUB for Kobo (Settings → Book server): the shelf's Kobo download
		// and the KEPUB link in the OPDS feeds. On by default.
		"books_kepub": a.deps.Settings.GetBool(ctx, kepub.KeyEnabled, true),
	}
	// "Show titles as" (displaytitles.go).
	for k, v := range a.displaySettings(ctx) {
		out[k] = v
	}
	// The audiobook and ebook folder/file templates (books_naming.go).
	for k, v := range a.bookNamingSettings(ctx) {
		out[k] = v
	}
	// The music templates, house tags and extras (music_naming.go).
	for k, v := range a.musicSettings(ctx) {
		out[k] = v
	}
	a.writeJSON(w, http.StatusOK, out)
}

// qualityRanking is the ranking mode in effect (quality_ranking: v2 unless "v1" was saved
// — the rollback), read the way the quality service reads it.
func (a *api) qualityRanking(ctx context.Context) string {
	return quality.ReadRanking(func(key, def string) string { return a.deps.Settings.Get(ctx, key, def) })
}

// bookLanguages is the saved book languages list (default English).
func (a *api) bookLanguages(ctx context.Context) []string {
	return automation.ParseBookLanguages(a.deps.Settings.Get(ctx, automation.KeyBookLanguages, automation.DefaultBookLanguages))
}

// parseBookLanguagesUpdate turns a PUT's list into the stored comma list, or says which
// entry isn't a language. Nothing unknown is stored: one bad code would reject every
// release that states its language.
func parseBookLanguagesUpdate(in []string) (string, string) {
	codes := make([]string, 0, len(in))
	seen := map[string]bool{}
	for _, tok := range in {
		l, ok := langs.Lookup(tok)
		if !ok {
			return "", "unknown language " + strconv.Quote(strings.TrimSpace(tok))
		}
		if !seen[l.Code] {
			seen[l.Code] = true
			codes = append(codes, l.Code)
		}
	}
	return strings.Join(codes, ","), ""
}

// maxPauseFreeGB bounds the disk guard's free-space limit (a petabyte, in GB): anything
// past it is a typo.
const maxPauseFreeGB = 1 << 20

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
	// NamingSeriesYearWhenNeeded: a new show folder keeps {year} only when it's needed.
	NamingSeriesYearWhenNeeded *bool `json:"naming_series_year_when_needed"`
	// Book naming (books_naming.go): checked as templates before anything is saved.
	NamingAudiobookFolder *string `json:"naming_audiobook_folder"`
	NamingAudiobookFile   *string `json:"naming_audiobook_file"`
	NamingAudiobookTrack  *string `json:"naming_audiobook_track"`
	NamingEbookFolder     *string `json:"naming_ebook_folder"`
	NamingEbookFile       *string `json:"naming_ebook_file"`
	// Music placement (music_naming.go): templates checked before anything is saved.
	NamingMusicFolder      *string `json:"naming_music_folder"`
	NamingMusicTrack       *string `json:"naming_music_track"`
	NamingMusicCompilation *string `json:"naming_music_compilation"`
	MusicWriteTags         *bool   `json:"music_write_tags"`
	MusicExtras            *string `json:"music_extras"`
	WriteNFO               *bool   `json:"write_nfo"`
	DownloadArtwork        *bool   `json:"download_artwork"`
	BooksEnabled           *bool   `json:"books_enabled"`
	MusicEnabled           *bool   `json:"music_enabled"`
	PlexLoginEnabled       *bool   `json:"plex_login_enabled"`
	PlexSignInStaff        *bool   `json:"plex_signin_staff"`
	TMDBRegion             *string `json:"tmdb_region"`
	PlexLoginAutoApprove   *bool   `json:"plex_login_auto_approve"` // legacy: every type or none
	// PlexLoginAutoApproveTypes wins over the legacy bool when both are sent.
	PlexLoginAutoApproveTypes *string   `json:"plex_login_auto_approve_types"`
	JellyfinLoginEnabled      *bool     `json:"jellyfin_login_enabled"`
	JellyfinQuickConnect      *bool     `json:"jellyfin_quickconnect_enabled"`
	JellyfinSignInStaff       *bool     `json:"jellyfin_signin_staff"`
	JellyfinAutoApproveTypes  *string   `json:"jellyfin_login_auto_approve_types"`
	RecycleMaxGB              *string   `json:"recycle_max_gb"`
	RecycleRetentionDays      *string   `json:"recycle_retention_days"`
	DiskGuard                 *bool     `json:"downloads_disk_guard"`
	DiskGuardPausePct         *string   `json:"downloads_disk_guard_pause_pct"`
	DiskGuardResumePct        *string   `json:"downloads_disk_guard_resume_pct"`
	DiskGuardMeasurePath      *string   `json:"downloads_disk_guard_measure_path"`
	DiskGuardPauseFreeGB      *string   `json:"downloads_disk_guard_pause_free_gb"`
	StallMinutes              *int      `json:"downloads_stall_minutes"`
	UpgradeBudget             *int      `json:"upgrade_max_grabs_per_sweep"`
	QuotaDays                 *int      `json:"request_quota_days"`
	QuotaMovies               *int      `json:"request_quota_movies"`
	QuotaSeasons              *int      `json:"request_quota_seasons"`
	QuotaBooks                *int      `json:"request_quota_books"`
	// Global seeding settings
	GlobalSeedEnabled        *bool     `json:"seeding_global_enabled"`
	GlobalSeedRatioLimit     *float64  `json:"seeding_global_ratio_limit"`
	GlobalMaxSeedingTime     *int      `json:"seeding_global_max_time_hours"`
	GlobalSeedingLimitAction *string   `json:"seeding_global_limit_action"`
	GlobalApplyToExisting    *bool     `json:"seeding_global_apply_to_existing"`
	DefaultCategories         *bool     `json:"indexer_default_categories"`
	BookLanguages             *[]string `json:"books_languages"`
	TitleLanguages            *[]string `json:"match_title_languages"`
	DisplayTitleMode          *string   `json:"display_title_mode"`
	DisplayTitleLanguage      *string   `json:"display_title_language"`
	DisplayTitleOriginalIn    *[]string `json:"display_title_original_in"`
	QualityRanking            *string   `json:"quality_ranking"`
	NetworkPreference         *string   `json:"download_network_preference"`
	PublicURL                 *string   `json:"app_public_url"`
	TwoFactorAdmins           *bool     `json:"two_factor_admins"`
	BooksKepub                *bool     `json:"books_kepub"`
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
	case boolChanged(req.JellyfinLoginEnabled, st.GetBool(ctx, keyJellyfinLoginEnabled, false)):
		return "Jellyfin sign-in"
	case boolChanged(req.JellyfinQuickConnect, a.jellyfinQuickConnectOn(ctx)):
		return "Jellyfin Quick Connect sign-in"
	case boolChanged(req.JellyfinSignInStaff, st.GetBool(ctx, keyJellyfinSignInStaff, false)):
		return "whether staff may sign in with Jellyfin"
	case req.JellyfinAutoApproveTypes != nil && auth.ParseAutoApproval(*req.JellyfinAutoApproveTypes) != a.jellyfinAutoApproval(ctx):
		return "auto-approval of Jellyfin sign-ins' requests"
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
	case strChanged(req.DiskGuardMeasurePath, st.Get(ctx, download.KeyDiskGuardMeasure, "")):
		// It names a server path, like the folders and the folder browser (admin-only).
		return "the drive the disk guard measures"
	case strChanged(req.DiskGuardPauseFreeGB, st.Get(ctx, download.KeyDiskGuardPauseFree, "0")):
		return "the disk guard's free-space limit"
	case quotaChanged(req, requests.GlobalLimits(func(key string) string { return st.Get(ctx, key, "") })):
		return "the request limits"
	case req.QualityRanking != nil && strings.TrimSpace(*req.QualityRanking) != a.qualityRanking(ctx):
		return "how releases are ranked"
	case req.PublicURL != nil && publicURLChanged(*req.PublicURL, st.PublicURL(ctx)):
		return "Arrmada's outside address"
	case boolChanged(req.TwoFactorAdmins, st.GetBool(ctx, keyTwoFactorAdmins, false)):
		return "whether admins must use two-factor sign-in"
	case boolChanged(req.BooksKepub, st.GetBool(ctx, kepub.KeyEnabled, true)):
		return "the Kobo (KEPUB) conversion"
	case boolChanged(req.GlobalSeedEnabled, st.GetBool(ctx, automation.KeyGlobalSeedEnabled, automation.DefaultGlobalSeedEnabled)):
		return "global seeding limits"
	case req.GlobalSeedRatioLimit != nil && *req.GlobalSeedRatioLimit != automation.ParseGlobalSeedRatioLimit(st.Get(ctx, automation.KeyGlobalSeedRatioLimit, "")):
		return "global seed ratio limit"
	case req.GlobalMaxSeedingTime != nil && *req.GlobalMaxSeedingTime != automation.ParseGlobalMaxSeedingTime(st.Get(ctx, automation.KeyGlobalMaxSeedingTime, "")):
		return "global max seeding time"
	case req.GlobalSeedingLimitAction != nil && strings.TrimSpace(strings.ToLower(*req.GlobalSeedingLimitAction)) != string(automation.ParseGlobalSeedingLimitAction(st.Get(ctx, automation.KeyGlobalSeedingLimitAction, ""))):
		return "global seeding limit action"
	case boolChanged(req.GlobalApplyToExisting, automation.ParseGlobalApplyToExisting(st.Get(ctx, automation.KeyGlobalApplyToExisting, ""))):
		return "apply global seeding to existing downloads"
	}
	return ""
}

// publicURLChanged reports whether a sent outside address differs from the one in effect
// (an address that doesn't pass the check counts as a change, so it's refused as one).
func publicURLChanged(sent, cur string) bool {
	v, err := settings.NormalizePublicURL(sent)
	return err != nil || v != cur
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
		// A value that isn't a number would otherwise read as the default below and be
		// stored as typed.
		for _, p := range []*string{req.DiskGuardPausePct, req.DiskGuardResumePct} {
			if p != nil && !wholeNumber(*p) {
				a.writeError(w, http.StatusBadRequest, "disk guard percentages must be whole numbers")
				return
			}
		}
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

	measurePath, measureChanged, msg := a.diskGuardMeasureUpdate(ctx, req.DiskGuardMeasurePath)
	if msg != "" {
		a.writeError(w, http.StatusBadRequest, msg)
		return
	}
	// Blank means off, stored as 0 so the field reads back the way it behaves.
	if req.DiskGuardPauseFreeGB != nil {
		v := strings.TrimSpace(*req.DiskGuardPauseFreeGB)
		if v == "" {
			v = "0"
		}
		if n, err := strconv.Atoi(v); err != nil || n < 0 || n > maxPauseFreeGB {
			a.writeError(w, http.StatusBadRequest, "the free-space limit must be a whole number of GB, from 0 (off) to "+strconv.Itoa(maxPauseFreeGB))
			return
		}
		req.DiskGuardPauseFreeGB = &v
	}

	if req.StallMinutes != nil && (*req.StallMinutes < 0 || *req.StallMinutes > quality.StallMaxMinutes) {
		a.writeError(w, http.StatusBadRequest, "the stall timeout must be between 0 (never) and 10080 minutes (a week)")
		return
	}
	if req.QualityRanking != nil && !quality.ValidRanking(strings.TrimSpace(*req.QualityRanking)) {
		a.writeError(w, http.StatusBadRequest, "quality_ranking must be v1 (classic) or v2 (ordered)")
		return
	}
	if req.NetworkPreference != nil && !quality.ValidNetwork(strings.TrimSpace(*req.NetworkPreference)) {
		a.writeError(w, http.StatusBadRequest, "download_network_preference must be best, usenet or torrent")
		return
	}
	if req.UpgradeBudget != nil && (*req.UpgradeBudget < 0 || *req.UpgradeBudget > maxUpgradeBudget) {
		a.writeError(w, http.StatusBadRequest, "upgrades per sweep must be between 0 (no limit) and "+strconv.Itoa(maxUpgradeBudget))
		return
	}
	// Global seeding settings validation
	if req.GlobalSeedRatioLimit != nil && *req.GlobalSeedRatioLimit < 0 {
		a.writeError(w, http.StatusBadRequest, "global seed ratio limit must be >= 0")
		return
	}
	if req.GlobalMaxSeedingTime != nil && *req.GlobalMaxSeedingTime < 0 {
		a.writeError(w, http.StatusBadRequest, "global max seeding time must be >= 0")
		return
	}
	if req.GlobalSeedingLimitAction != nil {
		v := strings.TrimSpace(strings.ToLower(*req.GlobalSeedingLimitAction))
		if v != "pause" && v != "stop" && v != "remove" {
			a.writeError(w, http.StatusBadRequest, "seeding limit action must be pause, stop, or remove")
			return
		}
	}
	// Blank isn't "unlimited": the bin reads anything unparseable as 0, so an emptied field
	// would quietly lift the cap. 0 has to be said.
	if req.RecycleMaxGB != nil && !wholeNumber(*req.RecycleMaxGB) {
		a.writeError(w, http.StatusBadRequest, "the recycle bin size cap must be a whole number of GB (0 = unlimited)")
		return
	}
	if req.RecycleRetentionDays != nil && !wholeNumber(*req.RecycleRetentionDays) {
		a.writeError(w, http.StatusBadRequest, "the recycle bin's keep-for must be a whole number of days (0 = keep forever)")
		return
	}
	// An empty template falls back to the default at import time, which isn't what
	// clearing the field looks like it does; say so instead of storing it.
	for _, p := range []*string{req.NamingMovieFolder, req.NamingMovieFile, req.NamingSeriesFolder, req.NamingSeriesSeason, req.NamingSeriesEpisode} {
		if p != nil && strings.TrimSpace(*p) == "" {
			a.writeError(w, http.StatusBadRequest, "a name template can't be empty")
			return
		}
	}
	if msg := checkBookNaming(&req); msg != "" {
		a.writeError(w, http.StatusBadRequest, msg)
		return
	}
	if msg := checkMusicSettings(&req); msg != "" {
		a.writeError(w, http.StatusBadRequest, msg)
		return
	}
	if msg := validQuota(req.QuotaDays, req.QuotaMovies, req.QuotaSeasons, req.QuotaBooks); msg != "" {
		a.writeError(w, http.StatusBadRequest, msg)
		return
	}
	var publicURL string
	if req.PublicURL != nil {
		v, err := settings.NormalizePublicURL(*req.PublicURL)
		if err != nil {
			a.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		publicURL = v
	}
	var bookLangs string
	if req.BookLanguages != nil {
		var msg string
		if bookLangs, msg = parseBookLanguagesUpdate(*req.BookLanguages); msg != "" {
			a.writeError(w, http.StatusBadRequest, msg)
			return
		}
	}
	var titleLangs string
	if req.TitleLanguages != nil {
		var msg string
		if titleLangs, msg = parseBookLanguagesUpdate(*req.TitleLanguages); msg != "" {
			a.writeError(w, http.StatusBadRequest, msg)
			return
		}
	}
	display, msg := a.parseDisplayUpdate(ctx, &req)
	if msg != "" {
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
	if req.PublicURL != nil && !save(a.deps.Settings.Set(ctx, settings.KeyPublicURL, publicURL)) {
		return
	}
	if req.BooksKepub != nil && !save(a.deps.Settings.SetBool(ctx, kepub.KeyEnabled, *req.BooksKepub)) {
		return
	}
	if req.DefaultCategories != nil && !save(a.deps.Settings.SetBool(ctx, indexer.KeyDefaultCategories, *req.DefaultCategories)) {
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
	if req.NamingSeriesYearWhenNeeded != nil && !save(a.deps.Settings.SetBool(ctx, settings.KeyNamingSeriesYearWhenNeeded, *req.NamingSeriesYearWhenNeeded)) {
		return
	}
	for _, f := range bookNamingFields {
		if p := bookNamingUpdate(&req)[f.key]; p != nil && !save(a.deps.Settings.Set(ctx, f.key, *p)) {
			return
		}
	}
	if !save(a.saveMusicSettings(ctx, &req)) {
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
	if req.TwoFactorAdmins != nil && !save(a.deps.Settings.SetBool(ctx, keyTwoFactorAdmins, *req.TwoFactorAdmins)) {
		return
	}
	if req.TMDBRegion != nil && !save(a.deps.Settings.Set(ctx, "tmdb_region", region)) {
		return
	}
	if types, ok := plexAutoApproveUpdate(&req); ok && !save(a.deps.Settings.Set(ctx, keyPlexAutoApproveTypes, types)) {
		return
	}
	for key, v := range map[string]*bool{
		keyJellyfinLoginEnabled: req.JellyfinLoginEnabled, keyJellyfinQuickConnect: req.JellyfinQuickConnect,
		keyJellyfinSignInStaff: req.JellyfinSignInStaff,
	} {
		if v != nil && !save(a.deps.Settings.SetBool(ctx, key, *v)) {
			return
		}
	}
	if req.JellyfinAutoApproveTypes != nil && !save(a.deps.Settings.Set(ctx, keyJellyfinAutoApproveTypes, auth.ParseAutoApproval(*req.JellyfinAutoApproveTypes).String())) {
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
	if measureChanged && !save(a.deps.Settings.Set(ctx, download.KeyDiskGuardMeasure, measurePath)) {
		return
	}
	if req.DiskGuardPauseFreeGB != nil && !save(a.deps.Settings.Set(ctx, download.KeyDiskGuardPauseFree, *req.DiskGuardPauseFreeGB)) {
		return
	}
	if req.StallMinutes != nil && !save(a.deps.Settings.Set(ctx, automation.KeyStallMinutes, strconv.Itoa(*req.StallMinutes))) {
		return
	}
	if req.UpgradeBudget != nil && !save(a.deps.Settings.Set(ctx, automation.KeyUpgradeBudget, strconv.Itoa(*req.UpgradeBudget))) {
		return
	}
	if req.GlobalSeedEnabled != nil && !save(a.deps.Settings.SetBool(ctx, automation.KeyGlobalSeedEnabled, *req.GlobalSeedEnabled)) {
		return
	}
	if req.GlobalSeedRatioLimit != nil && !save(a.deps.Settings.Set(ctx, automation.KeyGlobalSeedRatioLimit, strconv.FormatFloat(*req.GlobalSeedRatioLimit, 'f', -1, 64))) {
		return
	}
	if req.GlobalMaxSeedingTime != nil && !save(a.deps.Settings.Set(ctx, automation.KeyGlobalMaxSeedingTime, strconv.Itoa(*req.GlobalMaxSeedingTime))) {
		return
	}
	if req.GlobalSeedingLimitAction != nil && !save(a.deps.Settings.Set(ctx, automation.KeyGlobalSeedingLimitAction, strings.TrimSpace(strings.ToLower(*req.GlobalSeedingLimitAction)))) {
		return
	}
	if req.GlobalApplyToExisting != nil && !save(a.deps.Settings.SetBool(ctx, automation.KeyGlobalApplyToExisting, *req.GlobalApplyToExisting)) {
		return
	}
	if req.BookLanguages != nil && !save(a.deps.Settings.Set(ctx, automation.KeyBookLanguages, bookLangs)) {
		return
	}
	// The page sends every setting on each save: only a real change re-reads titles.
	if req.TitleLanguages != nil && titleLangs != a.deps.Settings.Get(ctx, settings.KeyTitleLanguages, "") {
		if !save(a.deps.Settings.Set(ctx, settings.KeyTitleLanguages, titleLangs)) {
			return
		}
		a.titleLanguagesChanged()
	}
	// The page sends one control's keys per save: only a real change re-works titles.
	if displayChanged, ok := a.saveDisplay(ctx, display); !ok {
		a.writeError(w, http.StatusInternalServerError, "could not save settings")
		return
	} else if displayChanged {
		a.displayPolicyChanged()
	}
	if req.QualityRanking != nil && !save(a.deps.Settings.Set(ctx, quality.KeyRanking, strings.TrimSpace(*req.QualityRanking))) {
		return
	}
	if req.NetworkPreference != nil && !save(a.deps.Settings.Set(ctx, quality.KeyNetworkPreference, strings.TrimSpace(*req.NetworkPreference))) {
		return
	}
	a.handleGetSettings(w, r)
}

// diskGuardMeasureUpdate checks a sent "measure a different drive" path: the path to store
// and whether it changed, or why it's refused. Only a change is checked, so a saved path
// whose mount has since gone doesn't refuse a save that sends it back unchanged.
func (a *api) diskGuardMeasureUpdate(ctx context.Context, sent *string) (string, bool, string) {
	if sent == nil {
		return "", false, ""
	}
	p := strings.TrimSpace(*sent)
	if p == strings.TrimSpace(a.deps.Settings.Get(ctx, download.KeyDiskGuardMeasure, "")) {
		return "", false, ""
	}
	if p == "" {
		return "", true, ""
	}
	if a.deps.DiskGuard == nil {
		return "", false, "the disk guard isn't running"
	}
	clean, err := a.deps.DiskGuard.CheckMeasurePath(p)
	if err != nil {
		return "", false, err.Error()
	}
	return clean, true, ""
}

// wholeNumber reports whether v (spaces trimmed) is a non-negative whole number.
func wholeNumber(v string) bool {
	n, err := strconv.Atoi(strings.TrimSpace(v))
	return err == nil && n >= 0
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
