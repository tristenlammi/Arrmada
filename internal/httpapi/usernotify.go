package httpapi

import (
	"net/http"

	"github.com/tristenlammi/arrmada/internal/requests"
)

// handleMyNotifications returns the current user's in-app inbox + unread count.
func (a *api) handleMyNotifications(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	items, err := a.deps.Requests.Inbox(r.Context(), u.ID)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load notifications")
		return
	}
	if items == nil {
		items = []requests.UserNotification{}
	}
	unread := 0
	for _, n := range items {
		if !n.Read {
			unread++
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"notifications": items, "unread": unread})
}

// handleMarkNotificationRead marks one inbox item read.
func (a *api) handleMarkNotificationRead(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if err := a.deps.Requests.MarkRead(r.Context(), id, u.ID); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update notification")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMarkAllNotificationsRead marks the current user's whole inbox read.
func (a *api) handleMarkAllNotificationsRead(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	if err := a.deps.Requests.MarkAllRead(r.Context(), u.ID); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update notifications")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleGetMyApprise returns whether the current user has a personal Apprise URL set.
func (a *api) handleGetMyApprise(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	url, err := a.deps.Requests.GetApprise(r.Context(), u.ID)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load setting")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"url": url, "set": url != ""})
}

// handleSetMyApprise sets the current user's personal Apprise URL (empty clears it).
func (a *api) handleSetMyApprise(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	var req struct {
		URL string `json:"url"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	if err := a.deps.Requests.SetApprise(r.Context(), u.ID, req.URL); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not save setting")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"url": req.URL, "set": req.URL != ""})
}
