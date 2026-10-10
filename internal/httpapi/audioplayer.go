package httpapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/listening"
)

// Arrmada's own listening API, for the web player (the Listen tab, the mini-player). It
// reads and moves the same places as the listening apps, through the same listening
// guards (audioserver.SyncSession is the very path an app's session sync takes), so a
// place is kept the same way whichever one plays. Every route is for the signed-in
// person only (it never takes a user id), works from outside the network for
// requesters, and needs the audiobook server switched on and the account allowed (403
// "Audiobooks are switched off" / "Your account isn't set up for audiobooks"). The
// request log records route patterns ("GET /api/v1/me/audio/items/{key}"), never the
// item: admins see how much and when people listen, never what.
//
// Read side:
//
//	GET    /api/v1/me/audio/shelves                       → {shelves: [{id, label, items: [Card]}]}
//	GET    /api/v1/me/audio/library?q=&sort=&filter=&page=&limit=
//	                                                      → {items: [Card], total, page, limit}
//	GET    /api/v1/me/audio/items/{key}                   → ItemDetail (chapters, tracks, versions, bookmarks, progress)
//	GET    /api/v1/me/audio/items/{key}/cover             → the image
//
// Play side:
//
//	POST   /api/v1/me/audio/items/{key}/play              {device_id?, device_name?} → PlayStart
//	POST   /api/v1/me/audio/sessions/{sid}/sync           {current_time?, time_listened, duration} → SyncResult
//	POST   /api/v1/me/audio/sessions/{sid}/close          (same; the body may be empty)
//	GET    /api/v1/me/audio/items/{key}/file/{ino}        → audio, Range supported
//	GET    /api/v1/me/audio/items/{key}/bookmarks         → {bookmarks}
//	POST   /api/v1/me/audio/items/{key}/bookmarks         {time, title} → bookmark
//	DELETE /api/v1/me/audio/items/{key}/bookmarks/{time}  → 204
//
// The contract the player keeps: start a session with play and play from start_time
// (tracks[].url, each starting at start_offset in the book). While playing, sync every
// 15 s; close on pause, on page hide and when the book ends. time_listened is the
// wall-clock seconds actually played since the last report (the server caps it by wall
// time). Leave current_time out when the player doesn't know where it is — that never
// moves the place. A sync answers the saved place: a big jump is held (held_position)
// until ~30 s of listening carries on from it (or, for a jump ahead into the last ten
// minutes, until it plays on to the end), exactly as for the apps. restart in PlayStart
// means a finished book is starting again from 0:00 ("Listen again"). A session id from
// another account answers 404.
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

// --- play side ----------------------------------------------------------------

// handleAudioPlay — POST /api/v1/me/audio/items/{key}/play {device_id?, device_name?}
func (a *api) handleAudioPlay(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioListener(w, r)
	if !ok {
		return
	}
	var req struct {
		DeviceID   string `json:"device_id"`
		DeviceName string `json:"device_name"`
	}
	if !a.decodeOptionalJSON(w, r, &req) {
		return
	}
	name := req.DeviceName
	if strings.TrimSpace(name) == "" {
		name = auth.UASummary(r.UserAgent())
	}
	start, err := a.deps.AudioServer.StartSession(r.Context(), u.ID, r.PathValue("key"), req.DeviceID, name)
	if err != nil {
		a.audioItemErr(w, err)
		return
	}
	a.writeJSON(w, http.StatusOK, start)
}

// audioSyncBody is a web-player report. current_time is a pointer on purpose: a report
// that doesn't say where the player is (a close on page hide, say) must never be read
// as 0:00.
type audioSyncBody struct {
	CurrentTime  *float64 `json:"current_time"`
	TimeListened float64  `json:"time_listened"`
	Duration     float64  `json:"duration"`
}

func (a *api) handleAudioSync(w http.ResponseWriter, r *http.Request)  { a.audioSync(w, r, false) }
func (a *api) handleAudioClose(w http.ResponseWriter, r *http.Request) { a.audioSync(w, r, true) }

// audioSync — POST /api/v1/me/audio/sessions/{sid}/sync and /close
// {current_time?, time_listened, duration} → {position, held_position, finished, duration}
func (a *api) audioSync(w http.ResponseWriter, r *http.Request, closeIt bool) {
	u, ok := a.audioListener(w, r)
	if !ok {
		return
	}
	var body audioSyncBody
	if !a.decodeOptionalJSON(w, r, &body) {
		return
	}
	if p := body.CurrentTime; p != nil && (math.IsNaN(*p) || math.IsInf(*p, 0)) {
		body.CurrentTime = nil
	}
	res, err := a.deps.AudioServer.SyncSession(r.Context(), u.ID, r.PathValue("sid"), body.CurrentTime, body.TimeListened, body.Duration, closeIt)
	if errors.Is(err, listening.ErrSessionNotFound) {
		a.writeError(w, http.StatusNotFound, "That listening session doesn't exist")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not save your place")
		return
	}
	a.writeJSON(w, http.StatusOK, res)
}

// handleAudioFile — GET /api/v1/me/audio/items/{key}/file/{ino}: one audio file, with
// Range for seeking. Same-origin <audio> sends the sign-in cookie, so no token in the URL.
func (a *api) handleAudioFile(w http.ResponseWriter, r *http.Request) {
	if _, ok := a.audioListener(w, r); !ok {
		return
	}
	a.deps.AudioServer.ServeFile(w, r, r.PathValue("key"), r.PathValue("ino"))
}

// audioBookItem checks the path's item is an audiobook in the library.
func (a *api) audioBookItem(w http.ResponseWriter, r *http.Request) (string, bool) {
	key := r.PathValue("key")
	if _, ok := a.deps.AudioServer.Info(r.Context(), key); !ok {
		a.audioItemErr(w, audioserver.ErrNotFound)
		return "", false
	}
	return key, true
}

// handleAudioBookmarks — GET /api/v1/me/audio/items/{key}/bookmarks → {bookmarks}
func (a *api) handleAudioBookmarks(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioListener(w, r)
	if !ok {
		return
	}
	key, ok := a.audioBookItem(w, r)
	if !ok {
		return
	}
	list, err := a.deps.AudioServer.Listen().Bookmarks(r.Context(), u.ID, key)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read your bookmarks")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"bookmarks": list})
}

// handleAddAudioBookmark — POST /api/v1/me/audio/items/{key}/bookmarks {time, title}.
// A bookmark at the same second is renamed rather than added twice.
func (a *api) handleAddAudioBookmark(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioListener(w, r)
	if !ok {
		return
	}
	key, ok := a.audioBookItem(w, r)
	if !ok {
		return
	}
	var req struct {
		Time  float64 `json:"time"`
		Title string  `json:"title"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if math.IsNaN(req.Time) || math.IsInf(req.Time, 0) || req.Time < 0 {
		a.writeError(w, http.StatusBadRequest, "time must be a number of seconds")
		return
	}
	title := strings.TrimSpace(req.Title)
	if utf8.RuneCountInString(title) > 200 {
		title = string([]rune(title)[:200])
	}
	b, err := a.deps.AudioServer.Listen().AddBookmark(r.Context(), u.ID, key, req.Time, title)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not save the bookmark")
		return
	}
	a.writeJSON(w, http.StatusOK, b)
}

// handleDeleteAudioBookmark — DELETE /api/v1/me/audio/items/{key}/bookmarks/{time}
func (a *api) handleDeleteAudioBookmark(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioListener(w, r)
	if !ok {
		return
	}
	key, ok := a.audioBookItem(w, r)
	if !ok {
		return
	}
	t, err := strconv.ParseFloat(r.PathValue("time"), 64)
	if err != nil || math.IsNaN(t) || math.IsInf(t, 0) {
		a.writeError(w, http.StatusBadRequest, "time must be a number of seconds")
		return
	}
	if err := a.deps.AudioServer.Listen().DeleteBookmark(r.Context(), u.ID, key, t); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not remove the bookmark")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// decodeOptionalJSON is decodeJSON for a body the client may leave empty (a close sent
// as the page is hidden, a play with nothing to say): no body decodes as nothing.
func (a *api) decodeOptionalJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	raw, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64<<10))
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return true
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid request body")
		return false
	}
	return true
}
