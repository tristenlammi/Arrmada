package httpapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/jobs"
	"github.com/tristenlammi/arrmada/internal/listening"
)

// The audiobook server's pages in Arrmada: the admin panel (switch it on, who may
// connect, devices, listening overview, Audiobookshelf import) and each user's own
// page (address, their audiobook password, devices, places with restore, listening).

func (a *api) audioHostPort() string {
	if p := strings.TrimSpace(os.Getenv("ARRMADA_AUDIOBOOK_HOST_PORT")); p != "" {
		return p
	}
	if a.deps.AudioManager != nil {
		return strings.TrimPrefix(a.deps.AudioManager.Addr(), ":")
	}
	return "13378"
}

func (a *api) audioConnection(r *http.Request) map[string]any {
	enabled := a.deps.Settings.GetBool(r.Context(), audioserver.KeyEnabled, false)
	running, lastErr := false, ""
	if a.deps.AudioManager != nil {
		running, lastErr = a.deps.AudioManager.Running()
	}
	return map[string]any{
		"enabled": enabled, "running": running, "error": lastErr,
		"host_port":  a.audioHostPort(),
		"public_url": a.deps.Settings.Get(r.Context(), audioserver.KeyPublicURL, ""),
	}
}

// handleAudioServer — GET /api/v1/audioserver (admin)
func (a *api) handleAudioServer(w http.ResponseWriter, r *http.Request) {
	if a.deps.AudioServer == nil {
		a.writeError(w, http.StatusServiceUnavailable, "audiobook server unavailable")
		return
	}
	ctx := r.Context()
	out := a.audioConnection(r)
	users, _ := a.deps.Auth.ListUsers(ctx)
	withPassword := a.deps.AudioServer.Accounts.PasswordsSet(ctx)
	denied := map[int64]bool{}
	for _, id := range a.deps.AudioServer.DeniedUsers(ctx) {
		denied[id] = true
	}
	list := []map[string]any{}
	for _, u := range users {
		list = append(list, map[string]any{
			"id": u.ID, "username": u.Username, "role": u.Role, "disabled": u.Disabled,
			"eligible": !u.Disabled && u.Role.AtLeast(auth.RoleRequester), "allowed": !denied[u.ID],
			"has_password": withPassword[u.ID],
		})
	}
	out["users"] = list
	devices, _ := a.deps.AudioServer.Accounts.Devices(ctx, 0)
	out["devices"] = devices
	items, ready := a.deps.AudioServer.LibraryStats(ctx)
	out["items"], out["items_ready"] = items, ready
	out["trace_until"] = a.deps.AudioServer.TraceUntil() // unix ms, 0 = off (it ends by itself)
	a.writeJSON(w, http.StatusOK, out)
}

// handleSetAudioServer — PUT /api/v1/audioserver (admin) {enabled?, public_url?, trace_hours?}.
// trace_hours (1–24) logs every request apps make, routes only, until it runs out; 0 stops.
func (a *api) handleSetAudioServer(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Enabled    *bool   `json:"enabled"`
		PublicURL  *string `json:"public_url"`
		TraceHours *int    `json:"trace_hours"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	ctx := r.Context()
	if req.TraceHours != nil {
		if *req.TraceHours < 0 || time.Duration(*req.TraceHours)*time.Hour > audioserver.MaxTrace || a.deps.AudioServer == nil {
			a.writeError(w, http.StatusBadRequest, "trace_hours must be between 0 and 24")
			return
		}
		if _, err := a.deps.AudioServer.SetTrace(ctx, time.Duration(*req.TraceHours)*time.Hour); err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not save")
			return
		}
	}
	if req.PublicURL != nil {
		_ = a.deps.Settings.Set(ctx, audioserver.KeyPublicURL, strings.TrimRight(strings.TrimSpace(*req.PublicURL), "/"))
	}
	if req.Enabled != nil {
		_ = a.deps.Settings.Set(ctx, audioserver.KeyEnabled, strconv.FormatBool(*req.Enabled))
		if a.deps.AudioManager != nil {
			a.deps.AudioManager.Apply(*req.Enabled)
		}
		if *req.Enabled && a.deps.AudioServer != nil {
			// Read the audiobooks' lengths and chapters now rather than on the first
			// app's first request.
			// Target "all": an audiobook-server job never names a book or a listener.
			_, _, _ = a.submit(r, jobs.Spec{Kind: "audioserver.warm", Target: "all", Timeout: 30 * time.Minute,
				Fn: errFn(func(ctx context.Context) error {
					a.deps.AudioServer.Warm(ctx)
					return nil
				})})
		}
	}
	a.handleAudioServer(w, r)
}

// handleSetAudioUser — PUT /api/v1/audioserver/users/{id} (admin) {allowed}
func (a *api) handleSetAudioUser(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Allowed bool `json:"allowed"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.AudioServer.SetAllowed(r.Context(), id, req.Allowed); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not save")
		return
	}
	a.handleAudioServer(w, r)
}

// handleRevokeAudioDevice — DELETE /api/v1/audioserver/devices/{family} (admin)
func (a *api) handleRevokeAudioDevice(w http.ResponseWriter, r *http.Request) {
	if err := a.deps.AudioServer.Accounts.RevokeFamily(r.Context(), r.PathValue("family"), 0); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not sign the device out")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleAudioListening — GET /api/v1/audioserver/listening?days=30&user_id= (admin).
// How much and when — never what: it reads a log with no book in it.
func (a *api) handleAudioListening(w http.ResponseWriter, r *http.Request) {
	userID, _ := strconv.ParseInt(r.URL.Query().Get("user_id"), 10, 64)
	a.writeListening(w, r, userID)
}

// handleMyAudioListening — GET /api/v1/me/audio/listening?days=30: the same overview,
// only ever of the signed-in person.
func (a *api) handleMyAudioListening(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioUser(w, r)
	if !ok {
		return
	}
	a.writeListening(w, r, u.ID)
}

// writeListening answers with totals, listening per day and sessions — for everyone when
// userID is 0, else one person.
func (a *api) writeListening(w http.ResponseWriter, r *http.Request, userID int64) {
	if a.deps.AudioServer == nil {
		a.writeError(w, http.StatusServiceUnavailable, "audiobook server unavailable")
		return
	}
	ctx := r.Context()
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	if days <= 0 || days > 90 {
		days = 30
	}
	now := time.Now()
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	since := dayStart.AddDate(0, 0, -(days - 1))
	store := a.deps.AudioServer.Listen()
	totals, err := store.Totals(ctx, dayStart)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read listening")
		return
	}
	daily, _ := store.Daily(ctx, since, userID)
	log, _ := store.Log(ctx, since, userID, 500)
	users, _ := a.deps.Auth.ListUsers(ctx)
	names := map[int64]string{}
	for _, u := range users {
		names[u.ID] = u.Username
	}
	tl := []map[string]any{}
	for id, t := range totals {
		if userID > 0 && id != userID {
			continue
		}
		tl = append(tl, map[string]any{"user_id": id, "username": names[id], "today": t.Today, "week": t.Week,
			"month": t.Month, "all_time": t.AllTime, "last_listen": t.LastListen})
	}
	sort.Slice(tl, func(i, j int) bool { return tl[i]["week"].(float64) > tl[j]["week"].(float64) })
	dl := []map[string]any{}
	for _, d := range daily {
		dl = append(dl, map[string]any{"user_id": d.UserID, "username": names[d.UserID], "day": d.Day, "seconds": d.Seconds})
	}
	sessions := []map[string]any{}
	for _, e := range log {
		sessions = append(sessions, map[string]any{"user_id": e.UserID, "username": names[e.UserID], "device": e.Device,
			"client": e.Client, "started_at": e.StartedAt, "ended_at": e.EndedAt, "seconds": e.Seconds})
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"totals": tl, "daily": dl, "sessions": sessions, "days": days,
		"since": since.Format("2006-01-02")})
}

func (a *api) absImportPath() string {
	return filepath.Join(a.deps.Config.DataDir, "audioserver", "abs-import.sqlite")
}

// handleAudioImportUpload — POST /api/v1/audioserver/import (admin, multipart "file").
// Saves the uploaded Audiobookshelf database and answers with a preview.
func (a *api) handleAudioImportUpload(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 2<<30)
	f, _, err := r.FormFile("file")
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "upload Audiobookshelf's absdatabase.sqlite as \"file\"")
		return
	}
	defer f.Close()
	path := a.absImportPath()
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	tmp := path + ".part"
	out, err := os.Create(tmp)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not save the upload")
		return
	}
	_, err = io.Copy(out, f)
	out.Close()
	if err != nil {
		_ = os.Remove(tmp)
		a.writeError(w, http.StatusBadRequest, "the upload didn't finish")
		return
	}
	_ = os.Rename(tmp, path)
	pv, err := a.deps.AudioServer.PreviewImport(r.Context(), path)
	if err != nil {
		_ = os.Remove(path)
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, pv)
}

// handleAudioImportApply — POST /api/v1/audioserver/import/apply (admin) {user_map}
func (a *api) handleAudioImportApply(w http.ResponseWriter, r *http.Request) {
	var req struct {
		UserMap map[string]int64 `json:"user_map"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	path := a.absImportPath()
	res, err := a.deps.AudioServer.ApplyImport(r.Context(), path, req.UserMap)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	_ = os.Remove(path)
	a.writeJSON(w, http.StatusOK, res)
}

// --- the signed-in user's own card ------------------------------------------

func (a *api) audioUser(w http.ResponseWriter, r *http.Request) (*auth.User, bool) {
	u, ok := userFrom(r)
	if !ok || u == nil || a.deps.AudioServer == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return nil, false
	}
	return u, true
}

// handleMyAudio — GET /api/v1/me/audio
func (a *api) handleMyAudio(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioUser(w, r)
	if !ok {
		return
	}
	ctx := r.Context()
	out := a.audioConnection(r)
	out["username"] = u.Username
	out["allowed"] = a.deps.AudioServer.Allowed(ctx, u)
	out["has_password"] = a.deps.AudioServer.Accounts.HasPassword(ctx, u.ID)
	out["min_password_length"] = audioserver.MinPasswordLength
	devs, _ := a.deps.AudioServer.Accounts.Devices(ctx, u.ID)
	out["devices"] = devs
	store := a.deps.AudioServer.Listen()
	progress, _ := store.AllProgress(ctx, u.ID)
	offers, _ := store.Offers(ctx, u.ID)
	places := []map[string]any{}
	for _, p := range progress {
		info, ok := a.deps.AudioServer.Info(ctx, p.ItemKey)
		if !ok {
			continue
		}
		// offer: a later spot an app sent that wasn't used ("use it?"), or null.
		var offer any
		if o, ok := offers[p.ItemKey]; ok {
			offer = map[string]any{"history_id": o.ID, "position": o.Position, "at": o.At, "device": o.Device, "reason": o.Reason}
		}
		places = append(places, map[string]any{"item_key": p.ItemKey, "book_id": info.BookID, "title": info.Title,
			"author": info.Author, "cover_url": info.CoverURL, "position": p.Position, "duration": p.Duration,
			"finished": p.Finished, "updated_at": p.UpdatedAt, "device": p.Device,
			"pending_position": p.PendingPosition, "pending_at": p.PendingAt, "offer": offer})
	}
	out["places"] = places
	// Places an app removed, still restorable ("Recently removed").
	discarded, _ := store.Discarded(ctx, u.ID)
	removed := []map[string]any{}
	for _, p := range discarded {
		info, ok := a.deps.AudioServer.Info(ctx, p.ItemKey)
		if !ok {
			continue
		}
		removed = append(removed, map[string]any{"item_key": p.ItemKey, "book_id": info.BookID, "title": info.Title,
			"author": info.Author, "cover_url": info.CoverURL, "position": p.Position, "duration": p.Duration,
			"finished": p.Finished, "discarded_at": p.DiscardedAt, "device": p.By})
	}
	out["removed"] = removed
	a.writeJSON(w, http.StatusOK, out)
}

// handleSetMyAudioPassword — PUT /api/v1/me/audio/password {password, sign_out_devices}.
// Setting one is what lets someone connect a listening app at all.
func (a *api) handleSetMyAudioPassword(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Password       string `json:"password"`
		SignOutDevices bool   `json:"sign_out_devices"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.AudioServer.Accounts.SetPassword(r.Context(), u.ID, req.Password, req.SignOutDevices); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.handleMyAudio(w, r)
}

// handleRemoveMyAudioPassword — DELETE /api/v1/me/audio/password: stops this person
// using the audiobook server and signs out their devices.
func (a *api) handleRemoveMyAudioPassword(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioUser(w, r)
	if !ok {
		return
	}
	if err := a.deps.AudioServer.Accounts.RemovePassword(r.Context(), u.ID); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not remove the password")
		return
	}
	a.handleMyAudio(w, r)
}

// handleRevokeMyDevice — DELETE /api/v1/me/audio/devices/{family}
func (a *api) handleRevokeMyDevice(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioUser(w, r)
	if !ok {
		return
	}
	if err := a.deps.AudioServer.Accounts.RevokeFamily(r.Context(), r.PathValue("family"), u.ID); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not sign the device out")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMyAudioHistory — GET /api/v1/me/audio/history?item=b12
func (a *api) handleMyAudioHistory(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioUser(w, r)
	if !ok {
		return
	}
	hist, err := a.deps.AudioServer.Listen().History(r.Context(), u.ID, r.URL.Query().Get("item"))
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read history")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"history": hist})
}

// handleMyAudioAccept — POST /api/v1/me/audio/accept {item}: confirm a held jump back.
func (a *api) handleMyAudioAccept(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Item string `json:"item"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	d, err := a.deps.AudioServer.Listen().AcceptPending(r.Context(), u.ID, req.Item)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"position": d.Progress.Position})
}

// handleMyAudioRestore — POST /api/v1/me/audio/restore {item, history_id}: put the place
// back to any row of its timeline. Using an offered later spot also settles the offer.
func (a *api) handleMyAudioRestore(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Item      string `json:"item"`
		HistoryID int64  `json:"history_id"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	d, err := a.deps.AudioServer.Listen().Restore(r.Context(), u.ID, req.Item, req.HistoryID)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"position": d.Progress.Position})
}

// handleMyAudioUndiscard — POST /api/v1/me/audio/undiscard {item}: put back a place an
// app removed, exactly as it was.
func (a *api) handleMyAudioUndiscard(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Item string `json:"item"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	store := a.deps.AudioServer.Listen()
	if err := store.Undiscard(r.Context(), u.ID, req.Item); err != nil {
		if errors.Is(err, listening.ErrNothingToRestore) {
			a.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not put the place back")
		return
	}
	p, _, _ := store.Progress(r.Context(), u.ID, req.Item)
	a.writeJSON(w, http.StatusOK, map[string]any{"position": p.Position})
}

// handleMyAudioDismiss — POST /api/v1/me/audio/dismiss {item, history_id}: say no to a
// later spot an app sent, so it isn't offered again.
func (a *api) handleMyAudioDismiss(w http.ResponseWriter, r *http.Request) {
	u, ok := a.audioUser(w, r)
	if !ok {
		return
	}
	var req struct {
		Item      string `json:"item"`
		HistoryID int64  `json:"history_id"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.AudioServer.Listen().Dismiss(r.Context(), u.ID, req.Item, req.HistoryID); err != nil {
		if errors.Is(err, listening.ErrNoHistory) {
			a.writeError(w, http.StatusNotFound, err.Error())
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not dismiss it")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleBookAudiobook — GET /api/v1/books/{id}/audiobook[?version=N]: the audiobook as a
// download — one file directly, several as a zip. Same access as ebook downloads.
func (a *api) handleBookAudiobook(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	b, err := a.deps.Books.Get(r.Context(), id)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if !a.mayDownloadBook(r, b) {
		a.writeError(w, http.StatusForbidden, "your account can't download books")
		return
	}
	if a.deps.AudioServer == nil {
		a.writeError(w, http.StatusServiceUnavailable, "audiobook downloads unavailable")
		return
	}
	vid, _ := strconv.ParseInt(r.URL.Query().Get("version"), 10, 64)
	if err := a.deps.AudioServer.WriteAudiobook(r.Context(), w, r, audioserver.ItemKey(id, vid)); err != nil {
		a.writeError(w, http.StatusNotFound, err.Error())
	}
}
