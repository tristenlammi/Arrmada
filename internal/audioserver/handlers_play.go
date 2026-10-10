package audioserver

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/listening"
	"github.com/tristenlammi/arrmada/internal/netutil"
)

type deviceInfo struct {
	ClientName    string `json:"clientName"`
	ClientVersion string `json:"clientVersion"`
	DeviceID      string `json:"deviceId"`
	DeviceName    string `json:"deviceName"`
	Model         string `json:"model"`
	Manufact      string `json:"manufacturer"`
}

func (d deviceInfo) name() string {
	switch {
	case d.DeviceName != "":
		return d.DeviceName
	case d.Model != "":
		return strings.TrimSpace(d.Manufact + " " + d.Model)
	}
	return ""
}

// handlePlay starts a play session at the user's saved place.
func (s *Server) handlePlay(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	u := userOf(r)
	it, err := s.item(ctx, r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Item not found")
		return
	}
	var body struct {
		DeviceInfo  deviceInfo `json:"deviceInfo"`
		MediaPlayer string     `json:"mediaPlayer"`
	}
	_ = readOptionalJSON(r, &body)
	client := firstNonEmpty(body.DeviceInfo.ClientName, clientName(r))
	device := firstNonEmpty(body.DeviceInfo.name(), client)
	sess, prog, err := s.listen.OpenSession(ctx, u.ID, it.Key, body.DeviceInfo.DeviceID, device, client)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't start playback")
		return
	}
	s.Accounts.NoteDevice(ctx, familyOf(r), device, client)
	var pp *listening.Progress
	if prog.UpdatedAt > 0 {
		pp = &prog
	}
	if sess.Restart {
		// Listening again from the start. Some apps seek to the item's progress rather
		// than the session's startTime, so that says 0:00, unfinished, too.
		view := prog
		view.Position, view.Finished, view.FinishedAt, view.PendingPosition = 0, false, 0, nil
		pp = &view
	}
	item, files := s.itemExpanded(ctx, it, pp)
	// The device as Audiobookshelf describes it back: its id here is the sign-in's
	// (one per device), and the address is the one the request came from.
	dev := obj{"id": familyOf(r), "userId": "u" + itoa(u.ID), "deviceId": body.DeviceInfo.DeviceID,
		"clientName": body.DeviceInfo.ClientName, "clientVersion": body.DeviceInfo.ClientVersion,
		"deviceName": body.DeviceInfo.DeviceName, "ipAddress": netutil.ClientIP(r)}
	if body.DeviceInfo.Manufact != "" {
		dev["manufacturer"] = body.DeviceInfo.Manufact
	}
	if body.DeviceInfo.Model != "" {
		dev["model"] = body.DeviceInfo.Model
	}
	writeJSON(w, http.StatusOK, s.sessionJSON(it, sess, item, files, body.MediaPlayer, dev))
}

func (s *Server) sessionJSON(it Item, sess listening.Session, item obj, files []AudioFile, player string, dev obj) obj {
	media := item["media"].(obj)
	started := time.UnixMilli(sess.StartedAt)
	return obj{
		"id": sess.ID, "userId": "u" + itoa(sess.UserID), "libraryId": libraryID, "libraryItemId": it.Key,
		"bookId": "m" + it.Key, "episodeId": nil, "mediaType": "book", "mediaMetadata": media["metadata"],
		"chapters": media["chapters"], "displayTitle": it.Title, "displayAuthor": it.Book.Author,
		"coverPath": media["coverPath"], "duration": media["duration"], "playMethod": 0, "mediaPlayer": player,
		"deviceInfo":    dev,
		"serverVersion": ServerVersion, "date": started.Format("2006-01-02"), "dayOfWeek": started.Weekday().String(),
		"timeListening": 0, "startTime": sess.StartPos, "currentTime": sess.StartPos,
		"startedAt": sess.StartedAt, "updatedAt": sess.LastAt, "audioTracks": media["tracks"], "libraryItem": item,
	}
}

type syncBody struct {
	CurrentTime  optNum  `json:"currentTime"`
	TimeListened flexNum `json:"timeListened"`
	Duration     flexNum `json:"duration"`
}

// optNum is a number that may be missing. Only a JSON number or a numeric string counts
// as present; a missing key, null, "" or anything unreadable is "not sent". A position
// that wasn't sent must never be read as 0:00 — that's how a bare close reset people.
type optNum struct {
	V  float64
	OK bool
}

func (o *optNum) UnmarshalJSON(b []byte) error {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	n, ok := 0.0, false
	switch t := v.(type) {
	case float64:
		n, ok = t, true
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			n, ok = f, true
		}
	}
	// "NaN" and "Inf" parse as numbers but aren't a place in a book.
	if ok && !math.IsNaN(n) && !math.IsInf(n, 0) {
		o.V, o.OK = n, true
	}
	return nil
}

// ptr is the value when it was sent, nil when it wasn't.
func (o optNum) ptr() *float64 {
	if !o.OK {
		return nil
	}
	v := o.V
	return &v
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request)  { s.sync(w, r, false) }
func (s *Server) handleClose(w http.ResponseWriter, r *http.Request) { s.sync(w, r, true) }

func (s *Server) sync(w http.ResponseWriter, r *http.Request, closeIt bool) {
	var body syncBody
	if err := readOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	pos := body.CurrentTime.ptr()
	d, err := s.syncSession(r.Context(), userOf(r).ID, r.PathValue("sid"), pos,
		float64(body.TimeListened), float64(body.Duration), closeIt)
	if errors.Is(err, listening.ErrSessionNotFound) {
		writeError(w, http.StatusNotFound, "Session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't save progress")
		return
	}
	if d.Reason == "held" {
		// Positions only: a username next to an item key would record who is listening to
		// what.
		s.log.Debug("audiobook server: holding a jump back until playback continues from it",
			"saved", d.Progress.Position, "reported", body.CurrentTime.V)
	}
	writeOK(w)
}

// syncSession applies one live report (pos nil: none sent) to a session, filling in the
// item's duration when the app didn't send one.
func (s *Server) syncSession(ctx context.Context, userID int64, sid string, pos *float64, listened, dur float64, closeIt bool) (listening.Decision, error) {
	if dur <= 0 {
		if sess, err := s.listen.GetSession(ctx, userID, sid); err == nil {
			dur = s.itemDuration(ctx, sess.ItemKey)
		}
	}
	return s.listen.Sync(ctx, userID, sid, pos, listened, dur, closeIt)
}

// itemDuration returns an item's total duration from the probe cache (0 if unknown).
func (s *Server) itemDuration(ctx context.Context, key string) float64 {
	it, err := s.item(ctx, key)
	if err != nil {
		return 0
	}
	files, _ := s.probe.files(ctx, it.Path, false)
	return totalDuration(files)
}

func (s *Server) handleGetSession(w http.ResponseWriter, r *http.Request) {
	sess, err := s.listen.GetSession(r.Context(), userOf(r).ID, r.PathValue("sid"))
	if err != nil {
		writeError(w, http.StatusNotFound, "Session not found")
		return
	}
	writeJSON(w, http.StatusOK, obj{"id": sess.ID, "libraryItemId": sess.ItemKey, "currentTime": sess.CurPos,
		"startTime": sess.StartPos, "timeListening": sess.Listened, "startedAt": sess.StartedAt, "updatedAt": sess.LastAt})
}

// localSession is an offline listening session an app uploads. Audiobookshelf takes
// whatever types an app sends, so these do too: a number may come as a string or with a
// fraction, a time as an ISO date, an id as a number.
type localSession struct {
	ID            flexString `json:"id"`
	LibraryItemID flexString `json:"libraryItemId"`
	EpisodeID     flexString `json:"episodeId"`
	Duration      flexNum    `json:"duration"`
	DeviceInfo    deviceInfo `json:"deviceInfo"`
	StartTime     flexNum    `json:"startTime"`
	CurrentTime   flexNum    `json:"currentTime"`
	TimeListening flexNum    `json:"timeListening"`
	StartedAt     flexNum    `json:"startedAt"`
	UpdatedAt     flexNum    `json:"updatedAt"`
	MediaPlayer   flexString `json:"mediaPlayer"`
}

// flexNum is a number sent as a number, a numeric string or an ISO time (read as Unix
// milliseconds). Anything else reads as zero rather than failing the whole upload.
type flexNum float64

func (f *flexNum) UnmarshalJSON(b []byte) error {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	switch t := v.(type) {
	case float64:
		*f = flexNum(t)
	case string:
		if n, err := strconv.ParseFloat(strings.TrimSpace(t), 64); err == nil {
			*f = flexNum(n)
		} else if tm, err := time.Parse(time.RFC3339Nano, strings.TrimSpace(t)); err == nil {
			*f = flexNum(tm.UnixMilli())
		}
	}
	return nil
}

// flexString is a string, or a number written as one; anything else reads as "".
type flexString string

func (f *flexString) UnmarshalJSON(b []byte) error {
	var v any
	if json.Unmarshal(b, &v) != nil {
		return nil
	}
	switch t := v.(type) {
	case string:
		*f = flexString(t)
	case float64:
		*f = flexString(strconv.FormatFloat(t, 'f', -1, 64))
	}
	return nil
}

// deviceInfo is read leniently too: a bad device block mustn't lose the sessions.
func (d *deviceInfo) UnmarshalJSON(b []byte) error {
	var raw map[string]flexString
	if json.Unmarshal(b, &raw) != nil {
		return nil
	}
	*d = deviceInfo{ClientName: string(raw["clientName"]), ClientVersion: string(raw["clientVersion"]), DeviceID: string(raw["deviceId"]),
		DeviceName: string(raw["deviceName"]), Model: string(raw["model"]), Manufact: string(raw["manufacturer"])}
	return nil
}

// applyLocal records one offline session; synced says whether it moved the saved place.
func (s *Server) applyLocal(ctx context.Context, r *http.Request, ls localSession, fallback deviceInfo) (synced bool, err error) {
	if ls.EpisodeID != "" {
		return false, errors.New("podcast episodes aren't served here")
	}
	it, err := s.item(ctx, string(ls.LibraryItemID))
	if err != nil {
		return false, errors.New("item not found")
	}
	di := ls.DeviceInfo
	if di.name() == "" {
		di = fallback
	}
	client := firstNonEmpty(di.ClientName, fallback.ClientName, clientName(r))
	d, err := s.listen.SyncOffline(ctx, userOf(r).ID, listening.OfflineSession{
		ID: string(ls.ID), ItemKey: it.Key, DeviceID: di.DeviceID, Device: firstNonEmpty(di.name(), client),
		Client: client, StartTime: float64(ls.StartTime), Position: float64(ls.CurrentTime), Duration: float64(ls.Duration),
		Listened: float64(ls.TimeListening), StartedAt: int64(ls.StartedAt), UpdatedAt: int64(ls.UpdatedAt),
	})
	return d.Changed, err
}

func (s *Server) handleLocalSession(w http.ResponseWriter, r *http.Request) {
	var ls localSession
	if err := readOptionalJSON(r, &ls); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	if _, err := s.applyLocal(r.Context(), r, ls, ls.DeviceInfo); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	writeOK(w)
}

// handleLocalAll takes a batch of offline sessions and answers each one by id, so the
// app knows exactly which to drop and which to retry.
func (s *Server) handleLocalAll(w http.ResponseWriter, r *http.Request) {
	// Like Audiobookshelf: no body, or no session list, is simply nothing to sync, and a
	// session that can't be read fails alone.
	var body struct {
		DeviceInfo deviceInfo        `json:"deviceInfo"`
		Sessions   []json.RawMessage `json:"sessions"`
	}
	var raw json.RawMessage
	if err := readOptionalJSON(r, &raw); err != nil {
		s.log.Info("audiobook server: unreadable offline sessions", "err", err, "client", r.UserAgent())
	}
	// Some apps (Plappa) send the sessions as a bare list rather than {"sessions": [...]}.
	if t := bytes.TrimSpace(raw); len(t) > 0 && t[0] == '[' {
		_ = json.Unmarshal(t, &body.Sessions)
	} else if len(t) > 0 {
		_ = json.Unmarshal(t, &body)
	}
	results := make([]obj, 0, len(body.Sessions))
	for _, raw := range body.Sessions {
		var ls localSession
		if err := json.Unmarshal(raw, &ls); err != nil {
			results = append(results, obj{"id": ls.ID, "success": false, "error": "unreadable session"})
			continue
		}
		synced, err := s.applyLocal(r.Context(), r, ls, body.DeviceInfo)
		if err != nil {
			results = append(results, obj{"id": ls.ID, "success": false, "error": err.Error()})
			continue
		}
		results = append(results, obj{"id": ls.ID, "success": true, "progressSynced": synced})
	}
	writeJSON(w, http.StatusOK, obj{"results": results})
}

func (s *Server) handleGetProgress(w http.ResponseWriter, r *http.Request) {
	p, ok, err := s.listen.Progress(r.Context(), userOf(r).ID, r.PathValue("id"))
	if err != nil || !ok {
		writeError(w, http.StatusNotFound, "No progress")
		return
	}
	writeJSON(w, http.StatusOK, mediaProgress(p))
}

type progressPatch struct {
	LibraryItemID string  `json:"libraryItemId"`
	CurrentTime   optNum  `json:"currentTime"`
	Duration      flexNum `json:"duration"`
	Progress      optNum  `json:"progress"`
	IsFinished    *bool   `json:"isFinished"`
	Hide          *bool   `json:"hideFromContinueListening"`
	LastUpdate    flexNum `json:"lastUpdate"` // when the app set it (unix ms)
}

// patchProgress applies an app's progress PATCH and returns the place actually kept —
// which, for a stale or held report, isn't the one the app sent.
func (s *Server) patchProgress(ctx context.Context, userID int64, key string, b progressPatch) (listening.Progress, error) {
	cur, _, err := s.listen.Progress(ctx, userID, key)
	if err != nil {
		return listening.Progress{}, err
	}
	dur := float64(b.Duration)
	if dur <= 0 {
		dur = cur.Duration
	}
	if dur <= 0 {
		dur = s.itemDuration(ctx, key)
	}
	pos, hasPos := cur.Position, false
	switch {
	case b.CurrentTime.OK:
		pos, hasPos = b.CurrentTime.V, true
	case b.Progress.OK && dur > 0:
		pos, hasPos = b.Progress.V*dur, true
	}
	unfinish := b.IsFinished != nil && !*b.IsFinished
	var d listening.Decision
	switch {
	case b.IsFinished != nil && *b.IsFinished:
		// Marking a book finished is the person's own action: applied at once.
		d, err = s.listen.SetProgress(ctx, userID, key, pos, dur, b.IsFinished, "app")
	case hasPos || (unfinish && cur.Finished):
		// Anything else an app sets goes through the same guards as a play session: an
		// older copy is ignored and a big jump back is held until playback carries on
		// from it. Apps often send isFinished:false on every update, so it only counts as
		// "mark not finished" when the book is finished.
		d, err = s.listen.ReportPosition(ctx, userID, key, pos, dur, unfinish, int64(b.LastUpdate), "app")
	default:
		d.Progress = cur
	}
	if err != nil {
		return listening.Progress{}, err
	}
	cur = d.Progress
	if b.Hide != nil {
		_ = s.listen.HideProgress(ctx, userID, key, *b.Hide)
		cur.Hidden = *b.Hide
	}
	return cur, nil
}

func (s *Server) handlePatchProgress(w http.ResponseWriter, r *http.Request) {
	var b progressPatch
	if !readJSON(w, r, &b) {
		return
	}
	key := r.PathValue("id")
	if _, err := s.item(r.Context(), key); err != nil {
		writeError(w, http.StatusNotFound, "Item not found")
		return
	}
	p, err := s.patchProgress(r.Context(), userOf(r).ID, key, b)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't save progress")
		return
	}
	writeJSON(w, http.StatusOK, mediaProgress(p))
}

func (s *Server) handleBatchProgress(w http.ResponseWriter, r *http.Request) {
	var list []progressPatch
	if !readJSON(w, r, &list) {
		return
	}
	for _, b := range list {
		if b.LibraryItemID == "" {
			continue
		}
		if it, err := s.item(r.Context(), b.LibraryItemID); err == nil {
			_, _ = s.patchProgress(r.Context(), userOf(r).ID, it.Key, b)
		}
	}
	writeOK(w)
}

func (s *Server) handleDeleteProgress(w http.ResponseWriter, r *http.Request) {
	// A soft delete: the app stops seeing the place, and the person can put it back from
	// Arrmada ("removed in Lissen").
	if err := s.listen.DeleteProgress(r.Context(), userOf(r).ID, r.PathValue("id"), firstNonEmpty(clientName(r), "app")); err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't remove progress")
		return
	}
	writeOK(w)
}

func (s *Server) handleHideProgress(w http.ResponseWriter, r *http.Request) {
	_ = s.listen.HideProgress(r.Context(), userOf(r).ID, r.PathValue("id"), true)
	writeJSON(w, http.StatusOK, s.meJSON(r))
}

func (s *Server) handleItemsInProgress(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	prog := s.progressMap(ctx, userOf(r).ID)
	all, _ := s.listen.AllProgress(ctx, userOf(r).ID)
	out := []obj{}
	for _, p := range all {
		if p.Finished || p.Hidden || p.Position <= 0 {
			continue
		}
		if it, err := s.item(ctx, p.ItemKey); err == nil {
			o := s.itemMinified(ctx, it, prog)
			o["progressLastUpdate"] = p.UpdatedAt
			out = append(out, o)
		}
	}
	writeJSON(w, http.StatusOK, obj{"libraryItems": out})
}

func (s *Server) handleAddBookmark(w http.ResponseWriter, r *http.Request) {
	var b struct {
		Time  float64 `json:"time"`
		Title string  `json:"title"`
	}
	if !readJSON(w, r, &b) {
		return
	}
	bm, err := s.listen.AddBookmark(r.Context(), userOf(r).ID, r.PathValue("id"), b.Time, b.Title)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't save the bookmark")
		return
	}
	writeJSON(w, http.StatusOK, bookmarkJSON(bm))
}

func (s *Server) handleDeleteBookmark(w http.ResponseWriter, r *http.Request) {
	t, err := strconv.ParseFloat(r.PathValue("time"), 64)
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid time")
		return
	}
	if err := s.listen.DeleteBookmark(r.Context(), userOf(r).ID, r.PathValue("id"), t); err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't remove the bookmark")
		return
	}
	writeOK(w)
}

// handleMyStats gives an app the user's own listening totals (no titles; the app knows
// what it played).
func (s *Server) handleMyStats(w http.ResponseWriter, r *http.Request) {
	u := userOf(r)
	now := time.Now()
	totals, _ := s.listen.Totals(r.Context(), time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()))
	t := totals[u.ID]
	writeJSON(w, http.StatusOK, obj{"totalTime": t.AllTime, "items": obj{}, "days": obj{}, "dayOfWeek": obj{},
		"today": t.Today, "recentSessions": []obj{}})
}

// readOptionalJSON decodes a body if there is one (some clients post nothing).
func readOptionalJSON(r *http.Request, dst any) error {
	if r.Body == nil || r.ContentLength == 0 {
		return nil
	}
	return decodeLenient(r, dst)
}
