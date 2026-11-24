package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tristenlammi/arrmada/internal/quality"
)

// handleQualityPreview scores a profile over the built-in sample set — the live
// feedback behind the builder. Accepts either a saved reference (?profile=) or a
// full profile spec in the POST body (for unsaved edits).
func (a *api) handleQualityPreview(w http.ResponseWriter, r *http.Request) {
	var sp quality.StoredProfile
	if r.Method == http.MethodPost {
		if !a.decodeJSON(w, r, &sp) {
			return
		}
	} else {
		ref := r.URL.Query().Get("profile")
		if ref == "" {
			ref = a.deps.Quality.DefaultProfile(r.Context(), quality.MediaMovie)
		}
		s, err := a.deps.Quality.GetStored(r.Context(), ref)
		if err != nil {
			a.writeError(w, http.StatusBadRequest, "unknown profile")
			return
		}
		sp = s
	}
	_, label := quality.Samples(sp.MediaType)
	// The editor's contradiction warnings ride on the preview, so they follow every unsaved
	// edit. A profile saved before targets existed is read the way the editor opens it.
	target := sp
	if target.Ideal == nil {
		target.Migrate()
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"profile":      sp.Name,
		"decision":     a.deps.Quality.Preview(sp),
		"sample_label": label, // what the samples are: a film, or an episode of a show
		"conflicts":    target.Conflicts(),
	})
}

// maxExplainName caps a pasted release name: real ones are well under it, and the parser
// and every format check run over the whole string.
const maxExplainName = 512

// handleQualityExplain judges one release name against a profile as edited, saved or not —
// the builder's "Test a release name" box: what the name parses to, the verdict, and which
// formats it matches.
//
//	{"profile": {...}, "name": "Show.S01E01.1080p.WEB.h264-GRP", "size_gb": 2, "runtime_min": 50}
func (a *api) handleQualityExplain(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Profile    quality.StoredProfile `json:"profile"`
		Name       string                `json:"name"`
		SizeGB     float64               `json:"size_gb"`
		RuntimeMin int                   `json:"runtime_min"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		a.writeError(w, http.StatusBadRequest, "paste a release name to test")
		return
	}
	if len(name) > maxExplainName {
		a.writeError(w, http.StatusBadRequest, "that release name is too long")
		return
	}
	a.writeJSON(w, http.StatusOK, a.deps.Quality.Explain(req.Profile, name, req.SizeGB, req.RuntimeMin))
}

// handleListQualityProfiles lists presets + user profiles for a media type.
func (a *api) handleListQualityProfiles(w http.ResponseWriter, r *http.Request) {
	media := r.URL.Query().Get("media")
	if media == "" {
		media = quality.MediaMovie
	}
	list, err := a.deps.Quality.List(r.Context(), media)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list profiles")
		return
	}
	if list == nil {
		list = []quality.ProfileInfo{}
	}
	body := map[string]any{"profiles": list, "formats": quality.Catalog(), "vocabulary": quality.ConditionVocabulary()}
	if media == quality.MediaMovie || media == quality.MediaSeries {
		// The Unwanted pack's rows and their defaults, for the Rules section.
		body["unwanted"] = quality.UnwantedCatalog()
		body["unwanted_groups"] = quality.DefaultLowQualityGroups
		// The release-group tiers a profile runs with until it edits its own.
		body["group_tiers"] = quality.DefaultGroupTiers()
	}
	if media == quality.MediaMusic {
		// Music quality is a ranked ladder rather than a set of preferences, so the builder
		// renders from the ladder and offers starter presets instead of the video formats.
		body["music_ladder"] = quality.MusicQualityLadder()
		body["music_presets"] = quality.MusicPresets()
	}
	a.writeJSON(w, http.StatusOK, body)
}

// handleGetQualityProfile returns an editable profile (preset or custom).
func (a *api) handleGetQualityProfile(w http.ResponseWriter, r *http.Request) {
	ref := r.PathValue("ref")
	sp, err := a.deps.Quality.GetStored(r.Context(), ref)
	if err != nil {
		a.writeError(w, http.StatusNotFound, "profile not found")
		return
	}
	a.writeJSON(w, http.StatusOK, sp)
}

// stallMinutesError explains a rejected stall timeout in the profile editor's own terms.
const stallMinutesError = "stall timeout must be -1 (off), 0 (use the default) or up to 10080 minutes (a week)"

// upgradeTriggerError explains a rejected "Replace for" choice.
const upgradeTriggerError = "upgrade_trigger must be any, source, format, resolution or language"

func (a *api) handleCreateQualityProfile(w http.ResponseWriter, r *http.Request) {
	var sp quality.StoredProfile
	if !a.decodeJSON(w, r, &sp) {
		return
	}
	if sp.Name == "" {
		a.writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if !quality.ValidStallMinutes(sp.StallMinutes) {
		a.writeError(w, http.StatusBadRequest, stallMinutesError)
		return
	}
	if !quality.ValidTrigger(sp.UpgradeTrigger) {
		a.writeError(w, http.StatusBadRequest, upgradeTriggerError)
		return
	}
	if msg := quality.UnwantedError(sp.Unwanted); msg != "" {
		a.writeError(w, http.StatusBadRequest, msg)
		return
	}
	if msg := quality.GroupTiersError(sp.GroupTiers, sp.Unwanted); msg != "" {
		a.writeError(w, http.StatusBadRequest, msg)
		return
	}
	created, err := a.deps.Quality.Create(r.Context(), sp)
	if invalid := (*quality.InvalidProfileError)(nil); errors.As(err, &invalid) {
		a.writeError(w, http.StatusBadRequest, invalid.Error())
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not create profile")
		return
	}
	a.writeJSON(w, http.StatusCreated, created)
}

func (a *api) handleUpdateQualityProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var sp quality.StoredProfile
	if !a.decodeJSON(w, r, &sp) {
		return
	}
	if !quality.ValidStallMinutes(sp.StallMinutes) {
		a.writeError(w, http.StatusBadRequest, stallMinutesError)
		return
	}
	if !quality.ValidTrigger(sp.UpgradeTrigger) {
		a.writeError(w, http.StatusBadRequest, upgradeTriggerError)
		return
	}
	if msg := quality.UnwantedError(sp.Unwanted); msg != "" {
		a.writeError(w, http.StatusBadRequest, msg)
		return
	}
	if msg := quality.GroupTiersError(sp.GroupTiers, sp.Unwanted); msg != "" {
		a.writeError(w, http.StatusBadRequest, msg)
		return
	}
	err := a.deps.Quality.Update(r.Context(), id, sp)
	if invalid := (*quality.InvalidProfileError)(nil); errors.As(err, &invalid) {
		a.writeError(w, http.StatusBadRequest, invalid.Error())
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update profile")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "updated"})
}

// handleSetDefaultProfile records the default profile for a media type.
func (a *api) handleSetDefaultProfile(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Media   string `json:"media"`
		Profile string `json:"profile"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Media == "" {
		req.Media = quality.MediaMovie
	}
	if err := a.deps.Quality.SetDefaultProfile(r.Context(), req.Media, req.Profile); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"media": req.Media, "profile": req.Profile})
}

func (a *api) handleDeleteQualityProfile(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	// ?move_to=custom:N picks where the profile's titles go; empty means the default.
	moved, to, err := a.deps.Quality.Delete(r.Context(), id, r.URL.Query().Get("move_to"))
	switch {
	case err == nil:
		a.writeJSON(w, http.StatusOK, map[string]any{"moved": moved, "moved_to": to})
	case errors.Is(err, quality.ErrNotFound):
		a.writeError(w, http.StatusNotFound, "profile not found")
	case errors.Is(err, quality.ErrLastProfile):
		a.writeError(w, http.StatusConflict, "Create another profile first")
	case errors.Is(err, quality.ErrMediaMismatch), errors.Is(err, quality.ErrSameProfile),
		errors.Is(err, quality.ErrTargetNotFound):
		a.writeError(w, http.StatusBadRequest, err.Error())
	default:
		a.writeError(w, http.StatusInternalServerError, "could not delete profile")
	}
}
