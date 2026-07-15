package httpapi

import (
	"net/http"

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
	a.writeJSON(w, http.StatusOK, map[string]any{
		"profile":  sp.Name,
		"decision": a.deps.Quality.Preview(sp),
	})
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
	a.writeJSON(w, http.StatusOK, map[string]any{"profiles": list, "formats": quality.Catalog()})
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

func (a *api) handleCreateQualityProfile(w http.ResponseWriter, r *http.Request) {
	var sp quality.StoredProfile
	if !a.decodeJSON(w, r, &sp) {
		return
	}
	if sp.Name == "" {
		a.writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	created, err := a.deps.Quality.Create(r.Context(), sp)
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
	if err := a.deps.Quality.Update(r.Context(), id, sp); err != nil {
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
	if err := a.deps.Quality.Delete(r.Context(), id); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not delete profile")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
