package httpapi

import (
	"errors"
	"net/http"

	"github.com/tristenlammi/arrmada/internal/notify"
)

// handleListNotifications returns all notification connections.
func (a *api) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Notify.List(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list notifications")
		return
	}
	if list == nil {
		list = []notify.Connection{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"notifications": list})
}

func (a *api) handleCreateNotification(w http.ResponseWriter, r *http.Request) {
	var c notify.Connection
	if !a.decodeJSON(w, r, &c) {
		return
	}
	if c.Name == "" || c.URL == "" {
		a.writeError(w, http.StatusBadRequest, "name and url are required")
		return
	}
	created, err := a.deps.Notify.Create(r.Context(), c)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not create notification")
		return
	}
	a.writeJSON(w, http.StatusCreated, created)
}

func (a *api) handleUpdateNotification(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var c notify.Connection
	if !a.decodeJSON(w, r, &c) {
		return
	}
	if err := a.deps.Notify.Update(r.Context(), id, c); err != nil {
		if errors.Is(err, notify.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "notification not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not update notification")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"status": "updated"})
}

func (a *api) handleDeleteNotification(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if err := a.deps.Notify.Delete(r.Context(), id); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not delete notification")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestNotification sends a sample message. Accepts either a saved id
// (path) or an unsaved connection in the body (to test before saving).
func (a *api) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	var c notify.Connection
	if !a.decodeJSON(w, r, &c) {
		return
	}
	if err := a.deps.Notify.Test(r.Context(), c); err != nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
