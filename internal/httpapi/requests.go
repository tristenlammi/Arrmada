package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/requests"
)

func (a *api) handleListRequests(w http.ResponseWriter, r *http.Request) {
	// Managers/admins see every request (to approve); everyone else sees only theirs.
	var scope int64
	autoApprove := false
	if u, ok := userFrom(r); ok {
		autoApprove = u.AutoApprove
		if !u.Role.AtLeast(auth.RoleManager) {
			scope = u.ID
		}
	}
	list, err := a.deps.Requests.List(r.Context(), r.URL.Query().Get("status"), scope)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list requests")
		return
	}
	if list == nil {
		list = []requests.Request{}
	}
	// Attach live download progress so the Discover "Your requests" row shows a bar.
	if queue, qerr := a.deps.Downloads.Queue(r.Context()); qerr == nil && len(queue) > 0 {
		for i := range list {
			if list[i].Available {
				continue
			}
			year := list[i].Year
			if list[i].MediaType == "series" {
				year = 0 // a series pack rarely carries the show's year
			}
			if p, ok := queueProgressByTitle(queue, list[i].Title, year); ok {
				list[i].DownloadProgress = p
			}
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"requests":     list,
		"auto_approve": autoApprove, // this viewer's own auto-approve status
	})
}

func (a *api) handleCreateRequest(w http.ResponseWriter, r *http.Request) {
	var req struct {
		MediaType      string `json:"media_type"`
		TMDBID         int    `json:"tmdb_id"`
		OLKey          string `json:"ol_key"`
		Title          string `json:"title"`
		Author         string `json:"author"`
		Year           int    `json:"year"`
		PosterURL      string `json:"poster_url"`
		Overview       string `json:"overview"`
		Note           string `json:"note"`
		QualityProfile string `json:"quality_profile"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	u, _ := userFrom(r)
	in := requests.Request{
		MediaType: req.MediaType, TMDBID: req.TMDBID, OLKey: req.OLKey, Title: req.Title, Author: req.Author, Year: req.Year,
		PosterURL: req.PosterURL, Overview: req.Overview, Note: req.Note, QualityProfile: req.QualityProfile,
		RequestedBy: u.ID, RequestedByName: u.Username,
	}
	// Auto-approve is a per-user property: this requester's request skips the queue
	// only if their account is set to auto-approve.
	created, err := a.deps.Requests.Create(r.Context(), in, u.AutoApprove)
	if errors.Is(err, requests.ErrExists) {
		a.writeJSON(w, http.StatusConflict, map[string]any{"error": "that title has already been requested", "request": created})
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusCreated, created)
}

func (a *api) handleApproveRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		QualityProfile string `json:"quality_profile"`
	}
	if r.Body != nil {
		_ = json.NewDecoder(r.Body).Decode(&req) // body is optional (profile override)
	}
	updated, err := a.deps.Requests.Approve(r.Context(), id, req.QualityProfile)
	if errors.Is(err, requests.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "request not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, updated)
}

func (a *api) handleDeclineRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if err := a.deps.Requests.Decline(r.Context(), id); err != nil {
		if errors.Is(err, requests.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "request not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not decline request")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "declined"})
}

func (a *api) handleDeleteRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if err := a.deps.Requests.Delete(r.Context(), id); err != nil {
		if errors.Is(err, requests.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "request not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not delete request")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
