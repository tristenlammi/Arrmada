package httpapi

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/plex"
)

// Linking a Plex account to the account you're signed in as: the admin and family accounts
// made by hand get their Plex watch history (Discover's "Recommended for you"), and the
// next Sign in with Plex finds this account instead of making another. It's the same PIN
// flow as Sign in with Plex, bound to this browser and this account.

// plexAccountID is the id a Plex account is linked by: its numeric plex.tv id (what
// Overseerr and Tautulli key on), or the uuid when plex.tv gave no number.
func plexAccountID(acct plex.Account) string {
	if acct.ID == 0 && acct.UUID != "" {
		return acct.UUID
	}
	return strconv.FormatInt(acct.ID, 10)
}

// handleMyPlex reports the signed-in account's Plex link.
func (a *api) handleMyPlex(w http.ResponseWriter, r *http.Request) {
	me, _ := userFrom(r)
	if me == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	l, err := a.deps.Auth.PlexLinkFor(r.Context(), me.ID)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not read your Plex link")
		return
	}
	a.writeJSON(w, http.StatusOK, l)
}

// handlePlexLinkStart starts a PIN for linking. ?mode=redirect comes back to
// /me?plexlink=<id>, where the Me page's Plex card finishes it.
func (a *api) handlePlexLinkStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me, _ := userFrom(r)
	if me == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	if !a.loginAllowed(w, r, "plexlink:"+strconv.FormatInt(me.ID, 10)) {
		return
	}
	redirect := plexRedirectMode(r)
	if _, ok := plexReturnBase(r); redirect && !ok {
		a.writeError(w, http.StatusBadRequest, "Couldn't work out this site's address for Plex to send you back to.")
		return
	}
	clientID := a.plexClientID(ctx)
	pin, err := plex.RequestPIN(ctx, clientID, plexLoginProduct)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	forward := ""
	if redirect {
		forward, _ = plexForwardURL(r, "/me?plexlink="+strconv.Itoa(pin.ID))
	}
	if err := a.bindPlexPin(w, r, plexFlowLink, me.ID, pin.ID); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not start linking Plex")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"id": pin.ID, "auth_url": plex.AuthURL(clientID, pin.Code, plexLoginProduct, forward)})
}

// handlePlexLinkPoll finishes a link once Plex has approved the PIN: {pending:true}
// until then, {linked, plex_username} after, or 409 when that Plex account belongs to
// another account. The Plex token is used once to learn who approved and is never kept.
func (a *api) handlePlexLinkPoll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	me, _ := userFrom(r)
	if me == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	pinID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid pin")
		return
	}
	if !a.plexPinOwned(r, plexFlowLink, me.ID, pinID) {
		a.writeError(w, http.StatusForbidden, errPlexPinElsewhere)
		return
	}
	clientID := a.plexClientID(ctx)
	token, err := plex.CheckPIN(ctx, clientID, pinID)
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if token == "" {
		a.writeJSON(w, http.StatusOK, map[string]any{"pending": true})
		return
	}
	defer a.plexPins.forget(pinID)
	acct, err := plex.GetAccount(ctx, clientID, token)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	err = a.deps.Auth.LinkPlex(ctx, me.ID, plexAccountID(acct), acct.Username)
	var taken *auth.ErrPlexAlreadyLinked
	if errors.As(err, &taken) {
		body := map[string]any{
			"status":            "error",
			"message":           "This Plex account is already linked to " + taken.Username + ".",
			"already_linked_to": taken.Username,
		}
		// An admin may move a duplicate requester's link (and what it holds) onto this
		// account: they get the id to merge. Nobody else learns ids, and a staff
		// account's link is never moved this way.
		if me.Role == auth.RoleAdmin && !taken.Role.AtLeast(auth.RoleManager) {
			body["user_id"] = taken.UserID
			body["mergeable"] = true
		}
		a.writeJSON(w, http.StatusConflict, body)
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not link your Plex account")
		return
	}
	a.deps.Log.Info("users: Plex account linked", "user_id", me.ID)
	a.writeJSON(w, http.StatusOK, map[string]any{"linked": true, "plex_username": acct.Username})
}

// handlePlexUnlink removes the signed-in account's Plex link, unless Plex is its only way in.
func (a *api) handlePlexUnlink(w http.ResponseWriter, r *http.Request) {
	me, _ := userFrom(r)
	if me == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	err := a.deps.Auth.UnlinkPlex(r.Context(), me.ID, true)
	if errors.Is(err, auth.ErrPlexOnlyLogin) {
		a.writeError(w, http.StatusConflict, "You sign in only with Plex, so unlinking it would lock you out. Ask an admin to set a password for your account first.")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not unlink Plex")
		return
	}
	a.deps.Log.Info("users: Plex account unlinked", "user_id", me.ID)
	a.writeJSON(w, http.StatusOK, auth.PlexLink{})
}
