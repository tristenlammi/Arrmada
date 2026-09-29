package audioserver

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/listening"
)

type deviceInfo struct {
	ClientName string `json:"clientName"`
	DeviceID   string `json:"deviceId"`
	DeviceName string `json:"deviceName"`
	Model      string `json:"model"`
	Manufact   string `json:"manufacturer"`
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
	item, files := s.itemExpanded(ctx, it, pp)
	writeJSON(w, http.StatusOK, s.sessionJSON(it, sess, item, files, body.MediaPlayer, body.DeviceInfo))
}

func (s *Server) sessionJSON(it Item, sess listening.Session, item obj, files []AudioFile, player string, di deviceInfo) obj {
	media := item["media"].(obj)
	started := time.UnixMilli(sess.StartedAt)
	return obj{
		"id": sess.ID, "userId": "u" + itoa(sess.UserID), "libraryId": libraryID, "libraryItemId": it.Key,
		"bookId": "m" + it.Key, "episodeId": nil, "mediaType": "book", "mediaMetadata": media["metadata"],
		"chapters": media["chapters"], "displayTitle": it.Title, "displayAuthor": it.Book.Author,
		"coverPath": media["coverPath"], "duration": media["duration"], "playMethod": 0, "mediaPlayer": player,
		"deviceInfo":    obj{"clientName": di.ClientName, "deviceId": di.DeviceID, "deviceName": di.DeviceName},
		"serverVersion": ServerVersion, "date": started.Format("2006-01-02"), "dayOfWeek": started.Weekday().String(),
		"timeListening": 0, "startTime": sess.StartPos, "currentTime": sess.StartPos,
		"startedAt": sess.StartedAt, "updatedAt": sess.LastAt, "audioTracks": media["tracks"], "libraryItem": item,
	}
}

type syncBody struct {
	CurrentTime  float64 `json:"currentTime"`
	TimeListened float64 `json:"timeListened"`
	Duration     float64 `json:"duration"`
}

func (s *Server) handleSync(w http.ResponseWriter, r *http.Request)  { s.sync(w, r, false) }
func (s *Server) handleClose(w http.ResponseWriter, r *http.Request) { s.sync(w, r, true) }

func (s *Server) sync(w http.ResponseWriter, r *http.Request, closeIt bool) {
	var body syncBody
	if err := readOptionalJSON(r, &body); err != nil {
		writeError(w, http.StatusBadRequest, "Invalid request body")
		return
	}
	ctx := r.Context()
	u := userOf(r)
	sid := r.PathValue("sid")
	dur := body.Duration
	if dur <= 0 {
		if sess, err := s.listen.GetSession(ctx, u.ID, sid); err == nil {
			dur = s.itemDuration(ctx, sess.ItemKey)
		}
	}
	d, err := s.listen.Sync(ctx, u.ID, sid, body.CurrentTime, body.TimeListened, dur, closeIt)
	if errors.Is(err, listening.ErrSessionNotFound) {
		writeError(w, http.StatusNotFound, "Session not found")
		return
	}
	if err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't save progress")
		return
	}
	if d.Reason == "held" {
		s.log.Debug("audiobook server: holding a jump back until playback continues from it",
			"user", u.Username, "item", d.Progress.ItemKey, "saved", d.Progress.Position, "reported", body.CurrentTime)
	}
	w.WriteHeader(http.StatusOK)
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

type localSession struct {
	ID            string     `json:"id"`
	LibraryItemID string     `json:"libraryItemId"`
	EpisodeID     *string    `json:"episodeId"`
	Duration      float64    `json:"duration"`
	DeviceInfo    deviceInfo `json:"deviceInfo"`
	StartTime     float64    `json:"startTime"`
	CurrentTime   float64    `json:"currentTime"`
	TimeListening float64    `json:"timeListening"`
	StartedAt     int64      `json:"startedAt"`
	UpdatedAt     int64      `json:"updatedAt"`
	MediaPlayer   string     `json:"mediaPlayer"`
}

func (s *Server) applyLocal(ctx context.Context, r *http.Request, ls localSession, fallback deviceInfo) error {
	if ls.EpisodeID != nil && *ls.EpisodeID != "" {
		return errors.New("podcast episodes aren't served here")
	}
	if _, err := s.item(ctx, ls.LibraryItemID); err != nil {
		return errors.New("item not found")
	}
	di := ls.DeviceInfo
	if di.name() == "" {
		di = fallback
	}
	client := firstNonEmpty(di.ClientName, fallback.ClientName, clientName(r))
	_, err := s.listen.SyncOffline(ctx, userOf(r).ID, listening.OfflineSession{
		ID: ls.ID, ItemKey: ls.LibraryItemID, DeviceID: di.DeviceID, Device: firstNonEmpty(di.name(), client),
		Client: client, StartTime: ls.StartTime, Position: ls.CurrentTime, Duration: ls.Duration,
		Listened: ls.TimeListening, StartedAt: ls.StartedAt, UpdatedAt: ls.UpdatedAt,
	})
	return err
}

func (s *Server) handleLocalSession(w http.ResponseWriter, r *http.Request) {
	var ls localSession
	if !readJSON(w, r, &ls) {
		return
	}
	if err := s.applyLocal(r.Context(), r, ls, ls.DeviceInfo); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}

// handleLocalAll takes a batch of offline sessions and answers each one by id, so the
// app knows exactly which to drop and which to retry.
func (s *Server) handleLocalAll(w http.ResponseWriter, r *http.Request) {
	var body struct {
		DeviceInfo deviceInfo     `json:"deviceInfo"`
		Sessions   []localSession `json:"sessions"`
	}
	if !readJSON(w, r, &body) {
		return
	}
	results := make([]obj, 0, len(body.Sessions))
	for _, ls := range body.Sessions {
		if err := s.applyLocal(r.Context(), r, ls, body.DeviceInfo); err != nil {
			results = append(results, obj{"id": ls.ID, "success": false, "error": err.Error()})
			continue
		}
		results = append(results, obj{"id": ls.ID, "success": true})
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
	LibraryItemID string   `json:"libraryItemId"`
	CurrentTime   *float64 `json:"currentTime"`
	Duration      float64  `json:"duration"`
	Progress      *float64 `json:"progress"`
	IsFinished    *bool    `json:"isFinished"`
	Hide          *bool    `json:"hideFromContinueListening"`
}

func (s *Server) patchProgress(ctx context.Context, userID int64, key string, b progressPatch) (listening.Progress, error) {
	cur, _, _ := s.listen.Progress(ctx, userID, key)
	dur := b.Duration
	if dur <= 0 {
		dur = cur.Duration
	}
	if dur <= 0 {
		dur = s.itemDuration(ctx, key)
	}
	pos := cur.Position
	switch {
	case b.CurrentTime != nil:
		pos = *b.CurrentTime
	case b.Progress != nil && dur > 0:
		pos = *b.Progress * dur
	}
	if b.CurrentTime != nil || b.Progress != nil || b.IsFinished != nil {
		d, err := s.listen.SetProgress(ctx, userID, key, pos, dur, b.IsFinished, "app")
		if err != nil {
			return listening.Progress{}, err
		}
		cur = d.Progress
	}
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
		if _, err := s.item(r.Context(), b.LibraryItemID); err == nil {
			_, _ = s.patchProgress(r.Context(), userOf(r).ID, b.LibraryItemID, b)
		}
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleDeleteProgress(w http.ResponseWriter, r *http.Request) {
	if err := s.listen.DeleteProgress(r.Context(), userOf(r).ID, r.PathValue("id")); err != nil {
		writeError(w, http.StatusInternalServerError, "Couldn't remove progress")
		return
	}
	w.WriteHeader(http.StatusOK)
}

func (s *Server) handleHideProgress(w http.ResponseWriter, r *http.Request) {
	_ = s.listen.HideProgress(r.Context(), userOf(r).ID, r.PathValue("id"), true)
	writeJSON(w, http.StatusOK, s.userJSON(r.Context(), userOf(r), nil))
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
	w.WriteHeader(http.StatusOK)
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
