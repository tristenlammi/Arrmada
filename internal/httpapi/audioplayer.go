package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/auth"
)

// Arrmada's own listening API, for the web player (the Listen tab, the mini-player). It
// reads and moves the same places as the listening apps, through the same listening
// guards, so a place is kept the same way whichever one plays. Every route is for the
// signed-in person only (it never takes a user id) and works from outside the network
// for requesters. The request log records route patterns ("GET
// /api/v1/me/audio/items/{key}"), never the item: admins see how much and when people
// listen, never what.
//
// Read side:
//   GET /api/v1/me/audio/shelves                → {shelves: [{id, label, items: [Card]}]}
//   GET /api/v1/me/audio/library?q=&sort=&filter=&page=&limit=
//                                               → {items: [Card], total, page, limit}
//   GET /api/v1/me/audio/items/{key}            → ItemDetail
//   GET /api/v1/me/audio/items/{key}/cover      → the image

// audioListener admits the signed-in person to the listening API: the audiobook server
// switched on and their account allowed to use it — the same one switch and allow-list
// as the listening apps.
func (a *api) audioListener(w http.ResponseWriter, r *http.Request) (*auth.User, bool) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return nil, false
	}
	if a.deps.AudioServer == nil || !a.deps.Settings.GetBool(r.Context(), audioserver.KeyEnabled, false) {
		a.writeError(w, http.StatusForbidden, "Audiobooks are switched off")
		return nil, false
	}
	if !a.deps.AudioServer.Allowed(r.Context(), u) {
		a.writeError(w, http.StatusForbidden, "Your account isn't set up for audiobooks")
		return nil, false
	}
	return u, true
}

// audioItemErr answers a failed item read without naming the item.
func (a *api) audioItemErr(w http.ResponseWriter, err error) {
	if errors.Is(err, audioserver.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "That audiobook isn't in the library")
		return
	}
	a.writeError(w, http.StatusInternalServerError, "could not read the audiobook")
}

// handleAudioShelves — GET /api/v1/me/audio/shelves
func (a *api) handleAudioShelves(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioListener(w, r)
	if !ok {
		return
	}
	shelves, err := a.deps.AudioServer.Shelves(r.Context(), u.ID)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the library")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"shelves": shelves})
}

// handleAudioLibrary — GET /api/v1/me/audio/library?q=&sort=&filter=&page=&limit=
func (a *api) handleAudioLibrary(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioListener(w, r)
	if !ok {
		return
	}
	q := r.URL.Query()
	page, _ := strconv.Atoi(q.Get("page"))
	limit, _ := strconv.Atoi(q.Get("limit"))
	cq := audioserver.CatalogQuery{Q: q.Get("q"), Sort: q.Get("sort"), Filter: q.Get("filter"), Page: page, Limit: limit}
	items, total, err := a.deps.AudioServer.Catalog(r.Context(), u.ID, cq)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read the library")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "page": max(page, 0), "limit": cq.PageSize()})
}

// handleAudioItem — GET /api/v1/me/audio/items/{key}
func (a *api) handleAudioItem(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioListener(w, r)
	if !ok {
		return
	}
	d, err := a.deps.AudioServer.Detail(r.Context(), u.ID, r.PathValue("key"))
	if err != nil {
		a.audioItemErr(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, d)
}

// handleAudioCover — GET /api/v1/me/audio/items/{key}/cover. The books cover route isn't
// reachable from outside the network; this one is, for anyone allowed to listen.
func (a *api) handleAudioCover(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.audioListener(w, r); !ok {
		return
	}
	a.deps.AudioServer.ServeCover(w, r, r.PathValue("key"))
}
