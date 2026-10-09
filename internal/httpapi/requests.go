package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/download"
	"github.com/tristenlammi/arrmada/internal/requests"
)

// Paging for GET /api/v1/requests: a page of 50 unless asked, never more than 200.
const (
	requestsPageDefault = 50
	requestsPageMax     = 200
	// requestNoteMax is the longest note a requester can attach, in characters.
	requestNoteMax = 500
)

// handleListRequests lists requests. Staff see everyone's; anyone else sees their own and
// the ones they follow, whatever the query asks — the scope is forced here.
//
//	GET /api/v1/requests?section=needs_approval|in_progress|ready|declined|all|strip
//	                    &limit=&offset=&media_type=movie|series|book&q=&status=
//
// It answers {requests, counts, total, auto_approve, client_health}. With no section and
// no limit it is the old list: everything, newest first, unpaged. section=strip is the
// Discover strip (requests.Service.Strip).
func (a *api) handleListRequests(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	staff := u.Role.AtLeast(auth.RoleManager)
	q := r.URL.Query()
	section := q.Get("section")
	f := requests.ListFilter{Status: q.Get("status"), MediaType: q.Get("media_type"), Query: q.Get("q")}
	if !staff {
		f.UserID, f.IncludeJoined = u.ID, true
	}
	switch f.MediaType {
	case "", "movie", "series", "book":
	default:
		a.writeError(w, http.StatusBadRequest, "media_type must be movie, series or book")
		return
	}
	var (
		list  []requests.Request
		total int
		err   error
	)
	if section == "strip" {
		list, err = a.deps.Requests.Strip(r.Context(), u.ID, staff)
		total = len(list)
	} else {
		if !requests.ValidSection(section) {
			a.writeError(w, http.StatusBadRequest, "unknown section")
			return
		}
		f.Section = section
		if (section != "" && section != requests.SectionAll) || q.Has("limit") {
			f.Limit = requestsPageDefault
			if n, perr := strconv.Atoi(q.Get("limit")); perr == nil && n > 0 {
				f.Limit = min(n, requestsPageMax)
			}
			if n, perr := strconv.Atoi(q.Get("offset")); perr == nil && n > 0 {
				f.Offset = n
			}
		}
		list, total, err = a.deps.Requests.List(r.Context(), f)
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list requests")
		return
	}
	// The counts ignore the section: every tab shows its own number.
	counts, err := a.deps.Requests.Counts(r.Context(), f)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not count requests")
		return
	}
	queueKnown := a.trackRequests(r, list)
	shapeRequests(list, staff)
	a.writeJSON(w, http.StatusOK, map[string]any{
		"requests":      list,
		"counts":        counts,
		"total":         total,
		"auto_approve":  u.AutoApprove, // this viewer's own auto-approve status
		"client_health": queueHealth{OK: queueKnown},
	})
}

// handleGetRequest is one request with its tracking. Staff can open any, and see who
// follows it; anyone else only one they made or follow (404 otherwise, the same answer
// as for a request that doesn't exist).
//
//	GET /api/v1/requests/{id}
func (a *api) handleGetRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	staff := u.Role.AtLeast(auth.RoleManager)
	req, err := a.deps.Requests.Detail(r.Context(), id, u.ID, staff)
	if errors.Is(err, requests.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "request not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load request")
		return
	}
	list := []requests.Request{req}
	queueKnown := a.trackRequests(r, list)
	shapeRequests(list, staff)
	a.writeJSON(w, http.StatusOK, map[string]any{"request": list[0], "client_health": queueHealth{OK: queueKnown}})
}

// handleUnsubscribeRequest stops the caller following someone else's request: they hear
// nothing more about it and it leaves their list. 404 when they don't follow it.
//
//	DELETE /api/v1/requests/{id}/subscription
func (a *api) handleUnsubscribeRequest(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	if err := a.deps.Requests.Unsubscribe(r.Context(), id, u.ID); err != nil {
		if errors.Is(err, requests.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "you don't follow that request")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not stop following the request")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleBulkRequests approves or declines up to 100 pending requests at once. Each is
// decided on its own, so the answer says how each one went: {results: [{id, ok, error}]}.
//
//	POST /api/v1/requests/bulk {action: approve|decline, ids, quality_profile?}
func (a *api) handleBulkRequests(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Action         string  `json:"action"`
		IDs            []int64 `json:"ids"`
		QualityProfile string  `json:"quality_profile"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if req.Action != requests.BulkApprove && req.Action != requests.BulkDecline {
		a.writeError(w, http.StatusBadRequest, "action must be approve or decline")
		return
	}
	// Each id once, in the order given.
	seen := map[int64]bool{}
	var ids []int64
	for _, id := range req.IDs {
		if id > 0 && !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		a.writeError(w, http.StatusBadRequest, "ids is required")
		return
	}
	if len(ids) > requests.BulkMax {
		a.writeError(w, http.StatusBadRequest, "at most 100 requests at once")
		return
	}
	u, _ := userFrom(r)
	var by int64
	var byName string
	if u != nil {
		by, byName = u.ID, u.Username
	}
	results := a.deps.Requests.Bulk(r.Context(), req.Action, ids, req.QualityProfile, by, byName)
	a.writeJSON(w, http.StatusOK, map[string]any{"results": results})
}

// trackRequests works out where each request has got to — searching, downloading (with
// progress), importing, ready — from the acquisition record, reading the download queue
// only when something on the page could be in flight. It reports whether the queue was
// known: when the client can't be read the cards say the status is unknown rather than
// "Searching", and a requester is told only that, never which client or why. A queue
// that can't be read never fails the page.
func (a *api) trackRequests(r *http.Request, list []requests.Request) bool {
	var queue []download.Item
	known := true
	if requests.NeedsQueue(list) {
		snap, ok, _ := a.queueSnapshot(r.Context())
		queue, known = snap.Items, ok
	}
	a.deps.Requests.Track(r.Context(), list, queue, known)
	return known
}

// shapeRequests readies requests for the viewer. Staff get the library item each became
// (for "Open in library"). A requester never sees another person: on a request they only
// follow, the owner's id, name and note are cleared.
func shapeRequests(list []requests.Request, staff bool) {
	for i := range list {
		if staff {
			list[i].LibraryID = list[i].InLibrary()
			continue
		}
		list[i].LibraryID = 0
		if list[i].Relation != requests.RelationOwner {
			list[i].RequestedBy, list[i].RequestedByName, list[i].Note = 0, "", ""
		}
	}
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
	// The note is the requester's word to whoever decides; keep it to a short message.
	req.Note = strings.TrimSpace(req.Note)
	if utf8.RuneCountInString(req.Note) > requestNoteMax {
		a.writeError(w, http.StatusBadRequest, "the note can be at most 500 characters")
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
	created, subscribed, err := a.deps.Requests.Create(r.Context(), in, requests.CreateOptions{AutoApprove: u.AutoApprove})
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
	o := requests.ApproveOptions{Profile: req.QualityProfile}
	if u, ok := userFrom(r); ok {
		o.DecidedBy, o.DecidedByName = u.ID, u.Username
	}
	updated, err := a.deps.Requests.Approve(r.Context(), id, o)
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
	var o requests.DeclineOptions
	if u, ok := userFrom(r); ok {
		o.DecidedBy, o.DecidedByName = u.ID, u.Username
	}
	if err := a.deps.Requests.Decline(r.Context(), id, o); err != nil {
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
