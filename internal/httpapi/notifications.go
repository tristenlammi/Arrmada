package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tristenlammi/arrmada/internal/notify"
)

// Admin alert connections. A saved Apprise URL never comes back out: the list carries a
// hint (scheme and, where it isn't secret, the host), an edit that leaves the URL out
// keeps the stored one, and a saved connection is tested by id. Staff can see the list;
// only admins change it or make it send.

// notificationView is a connection as the API shows it: everything but the URL.
type notificationView struct {
	notify.Connection
	URLHint string `json:"url_hint"`
	URLSet  bool   `json:"url_set"`
	// InvalidReason says why a URL saved before validation existed no longer passes it.
	// The connection still sends; editing it needs a valid URL.
	InvalidReason string `json:"invalid_reason,omitempty"`
}

func viewOf(c notify.Connection) notificationView {
	v := notificationView{Connection: c, URLHint: notify.URLHint(c.URL), URLSet: c.URL != ""}
	if c.URL != "" {
		if err := notify.ValidateAppriseURL(c.URL); err != nil {
			v.InvalidReason = err.Error()
		}
	}
	return v
}

// notificationInput is what create and update accept. URL is a pointer so an update can
// leave it out (or send "") to keep the stored one; Events likewise (a new connection
// left without them starts on the catalog's defaults).
type notificationInput struct {
	Name    string    `json:"name"`
	Kind    string    `json:"kind"`
	URL     *string   `json:"url"`
	Events  *[]string `json:"events"`
	Enabled bool      `json:"enabled"`
}

func (in notificationInput) url() string {
	if in.URL == nil {
		return ""
	}
	return strings.TrimSpace(*in.URL)
}

func (in notificationInput) apply(c *notify.Connection) {
	c.Name, c.Kind, c.Enabled = strings.TrimSpace(in.Name), in.Kind, in.Enabled
	if in.Events != nil {
		c.Events = append([]string{}, *in.Events...)
	}
}

// checkEvents refuses event keys the catalog doesn't know.
func (a *api) checkEvents(w http.ResponseWriter, in notificationInput) bool {
	if in.Events == nil {
		return true
	}
	if err := notify.ValidEvents(*in.Events); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return false
	}
	return true
}

// handleNotificationCatalog lists the events a connection can subscribe to, grouped
// for the Alerts page.
func (a *api) handleNotificationCatalog(w http.ResponseWriter, r *http.Request) {
	a.writeJSON(w, http.StatusOK, map[string]any{"groups": notify.Groups(), "events": notify.Catalog()})
}

// handleListNotifications returns all notification connections, URLs redacted.
func (a *api) handleListNotifications(w http.ResponseWriter, r *http.Request) {
	list, err := a.deps.Notify.List(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list notifications")
		return
	}
	out := make([]notificationView, 0, len(list))
	for _, c := range list {
		out = append(out, viewOf(c))
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"notifications": out})
}

func (a *api) handleCreateNotification(w http.ResponseWriter, r *http.Request) {
	var in notificationInput
	if !a.decodeJSON(w, r, &in) {
		return
	}
	if !a.checkEvents(w, in) {
		return
	}
	c := notify.Connection{Events: notify.DefaultEvents()}
	in.apply(&c)
	c.URL = in.url()
	if c.Name == "" || c.URL == "" {
		a.writeError(w, http.StatusBadRequest, "name and url are required")
		return
	}
	// Checked on save, not at send time: a typo or an option-looking string would
	// otherwise be stored and only fail when an alert is due.
	if err := notify.ValidateAppriseURL(c.URL); err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := a.deps.Notify.Create(r.Context(), c)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not create notification")
		return
	}
	a.writeJSON(w, http.StatusCreated, viewOf(created))
}

func (a *api) handleUpdateNotification(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var in notificationInput
	if !a.decodeJSON(w, r, &in) {
		return
	}
	c, err := a.deps.Notify.Get(r.Context(), id)
	if errors.Is(err, notify.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "notification not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load notification")
		return
	}
	if !a.checkEvents(w, in) {
		return
	}
	in.apply(&c)
	if c.Name == "" {
		a.writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	// No URL (or a blank one) keeps the stored URL, so renaming or ticking events
	// doesn't mean retyping a token. A new one must pass validation.
	if u := in.url(); u != "" {
		if err := notify.ValidateAppriseURL(u); err != nil {
			a.writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		c.URL = u
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
		if errors.Is(err, notify.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "notification not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not delete notification")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTestNotification sends a sample message to an unsaved connection in the body,
// so a URL can be tried before it's saved.
func (a *api) handleTestNotification(w http.ResponseWriter, r *http.Request) {
	var in notificationInput
	if !a.decodeJSON(w, r, &in) {
		return
	}
	var c notify.Connection
	in.apply(&c)
	c.URL = in.url()
	if err := notify.ValidateAppriseURL(c.URL); err != nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	a.answerTest(w, r, c)
}

// handleTestSavedNotification sends a sample message through a saved connection's
// stored URL, which the browser never sees.
func (a *api) handleTestSavedNotification(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	c, err := a.deps.Notify.Get(r.Context(), id)
	if errors.Is(err, notify.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "notification not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load notification")
		return
	}
	a.answerTest(w, r, c)
}

func (a *api) answerTest(w http.ResponseWriter, r *http.Request, c notify.Connection) {
	if err := a.deps.Notify.Test(r.Context(), c); err != nil {
		a.writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
