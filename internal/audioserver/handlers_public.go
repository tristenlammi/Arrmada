package audioserver

import (
	"net/http"
	"strconv"
	"time"
)

// streamSessionIdle is how long after its last report a play session's stream link
// keeps working. An app that pauses for longer starts a new session when it plays again.
const streamSessionIdle = 48 * time.Hour

// handleSessionTrack streams one track of an open play session:
// GET /public/session/{sid}/track/{index}. Audiobookshelf 2.22+ apps — the official
// app — play from this link without sending a token (AVPlayer can't add one), so the
// session id is the key: a random UUID only the device that pressed play was given,
// valid while the session is open and recently used, and only while its owner may still
// use the audiobook server.
func (s *Server) handleSessionTrack(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	sess, err := s.listen.StreamSession(ctx, r.PathValue("sid"), streamSessionIdle)
	if err != nil {
		writeError(w, http.StatusNotFound, "Session not found")
		return
	}
	u, err := s.users.UserByID(ctx, sess.UserID)
	if err != nil || !s.Allowed(ctx, u) || !s.Accounts.HasPassword(ctx, u.ID) {
		writeError(w, http.StatusNotFound, "Session not found")
		return
	}
	index, err := strconv.Atoi(r.PathValue("index"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "Invalid track index")
		return
	}
	it, err := s.item(ctx, sess.ItemKey)
	if err != nil {
		writeError(w, http.StatusNotFound, "Item not found")
		return
	}
	files, err := s.probe.files(ctx, it.Path, false)
	if err != nil {
		writeError(w, http.StatusNotFound, "Track not found")
		return
	}
	for _, f := range files {
		if f.Index == index {
			w.Header().Set("Content-Type", mimeFor(f.Ext))
			w.Header().Set("Cache-Control", "private, max-age=86400")
			http.ServeFile(w, r, f.Path)
			return
		}
	}
	writeError(w, http.StatusNotFound, "Track not found")
}
