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
	// Canonicalize display metadata from the TMDB id. Title/poster/overview came
	// straight from the client, so a requester could submit movie X's id dressed in
	// harmless movie Y's title and poster — and the manager would approve what the
	// spoofed card showed. Best-effort: if the lookup fails, the client values stand
	// (the approval flow re-fetches by id anyway, so the LIBRARY was never spoofable).
	if (in.MediaType == "movie" || in.MediaType == "series") && in.TMDBID > 0 && a.deps.Discovery != nil {
		if d, derr := a.deps.Discovery.MediaDetails(r.Context(), in.MediaType, in.TMDBID); derr == nil && d != nil {
			if d.Title != "" {
				in.Title = d.Title
			}
			if d.Year > 0 {
				in.Year = d.Year
			}
			if d.PosterURL != "" {
				in.PosterURL = d.PosterURL
			}
			if d.Overview != "" {
				in.Overview = d.Overview
			}
		}
	}
	// Auto-approve is a per-user property: this requester's request skips the queue
	// only if their account is set to auto-approve.
	created, subscribed, err := a.deps.Requests.Create(r.Context(), in, u.AutoApprove)
	if errors.Is(err, requests.ErrExists) {
		// Only reachable when the duplicate row vanished between detection and
		// re-fetch — vanishingly rare; the normal duplicate path subscribes instead.
		a.writeError(w, http.StatusConflict, "that title has already been requested")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	// subscribed=true means the caller was attached to an existing request rather
	// than creating (or resurrecting) one.
	a.writeJSON(w, http.StatusOK, map[string]any{"request": created, "subscribed": subscribed})
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
	if errors.Is(err, requests.ErrUnknownProfile) {
		a.writeError(w, http.StatusBadRequest, "unknown quality profile")
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
	// Managers can delete anything; everyone else may only withdraw their own
	// request while it's still pending. (The route admits any signed-in requester,
	// so ownership must be checked here.)
	if u, ok := userFrom(r); ok && !u.Role.AtLeast(auth.RoleManager) {
		req, err := a.deps.Requests.Get(r.Context(), id)
		if errors.Is(err, requests.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "request not found")
			return
		}
		if err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not load request")
			return
		}
		if req.RequestedBy != u.ID || req.Status != requests.StatusPending {
			a.writeError(w, http.StatusForbidden, "you can only withdraw your own pending requests")
			return
		}
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
