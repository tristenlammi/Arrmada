package httpapi

import (
	"context"
	"net"
	"net/http"
	"strings"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/notify"
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
	a.inboxPlexLinks(r.Context(), items)
	unread := 0
	for _, n := range items {
		if !n.Read {
			unread++
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"notifications": items, "unread": unread})
}

// inboxPlexLinks gives each 'ready' notice its Watch on Plex link: the title's page as the
// Plex index has it now (from memory — a notice sent during the grace period gains one
// once Plex has the title), else the one saved when it was sent. While Plex isn't set up
// or its index isn't built, every link is hidden, so the bell never offers a Watch button
// that can't work.
func (a *api) inboxPlexLinks(ctx context.Context, items []requests.UserNotification) {
	ready := a.plexReady(ctx)
	for i := range items {
		n := &items[i]
		if !ready {
			n.PlexURL = ""
			continue
		}
		if media, id, ok := requests.ReadyRef(n.Ref); ok {
			if u := a.watchURL(ctx, media, insights.ExternalIDs{TMDB: id}); u != "" {
				n.PlexURL = u
			}
		}
	}
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

// handleGetMyApprise says whether the current user has a personal Apprise URL set — a
// hint of it, never the URL — and why it's being skipped if it no longer passes the
// check for their account.
func (a *api) handleGetMyApprise(w http.ResponseWriter, r *http.Request) {
	u, ok := userFrom(r)
	if !ok || u == nil {
		a.writeError(w, http.StatusUnauthorized, "not signed in")
		return
	}
	a.writeMyApprise(w, r, u)
}

func (a *api) writeMyApprise(w http.ResponseWriter, r *http.Request, u *auth.User) {
	set, hint, blocked, err := a.deps.Requests.AppriseStatus(r.Context(), u.ID, u.Role.AtLeast(auth.RoleManager))
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load setting")
		return
	}
	out := map[string]any{"set": set, "hint": hint}
	if blocked != "" {
		out["blocked_reason"] = blocked
	}
	a.writeJSON(w, http.StatusOK, out)
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
	// Validate before storing: the URL becomes an apprise CLI argument on the server
	// and the server posts to it, so for a requester it must be a push service that
	// can't be aimed at the local network (notify/ssrf.go). Staff keep the ordinary
	// rules. ("" clears the setting and is always allowed.)
	req.URL = strings.TrimSpace(req.URL)
	if req.URL != "" {
		staff := u.Role.AtLeast(auth.RoleManager)
		if verr := notify.ValidateUserAppriseURL(r.Context(), req.URL, staff, a.resolver()); verr != nil {
			a.writeError(w, http.StatusBadRequest, verr.Error())
			return
		}
	}
	if err := a.deps.Requests.SetApprise(r.Context(), u.ID, req.URL); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not save setting")
		return
	}
	a.writeMyApprise(w, r, u)
}

// resolver is what personal Apprise URLs are checked against: the system resolver, or
// a stub a test put in Deps.
func (a *api) resolver() notify.Resolver {
	if a.deps.Resolver != nil {
		return a.deps.Resolver
	}
	return net.DefaultResolver
}
