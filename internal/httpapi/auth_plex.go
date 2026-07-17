package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/plex"
)

const plexLoginProduct = "Arrmada"

// plexClientID returns this instance's stable X-Plex-Client-Identifier, shared with Insights so
// there's one Plex identity for Arrmada. Generated + persisted on first use.
func (a *api) plexClientID(ctx context.Context) string {
	id := a.deps.Settings.Get(ctx, "insights_plex_client_id", "")
	if id == "" {
		b := make([]byte, 16)
		_, _ = rand.Read(b)
		id = hex.EncodeToString(b)
		_ = a.deps.Settings.Set(ctx, "insights_plex_client_id", id)
	}
	return id
}

// handlePlexLoginStart begins a Plex sign-in: returns a PIN id + the plex.tv URL to authorize it.
func (a *api) handlePlexLoginStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !a.deps.Settings.GetBool(ctx, "plex_login_enabled", false) {
		a.writeError(w, http.StatusForbidden, "Plex sign-in is disabled")
		return
	}
	clientID := a.plexClientID(ctx)
	pin, err := plex.RequestPIN(ctx, clientID, plexLoginProduct)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"id":       pin.ID,
		"auth_url": plex.AuthURL(clientID, pin.Code, plexLoginProduct),
	})
}

// handlePlexLoginPoll polls a PIN; once the user has authorized it, verifies they have access to
// this Plex server, provisions a requester account (auto-approve per settings), and starts a
// session. Returns {pending:true} until the user authorizes.
func (a *api) handlePlexLoginPoll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !a.deps.Settings.GetBool(ctx, "plex_login_enabled", false) {
		a.writeError(w, http.StatusForbidden, "Plex sign-in is disabled")
		return
	}
	pinID, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		a.writeError(w, http.StatusBadRequest, "invalid pin")
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

	// Authorization gate: the signing-in user must have access to THIS Plex server.
	adminToken := a.deps.Settings.Get(ctx, "insights_plex_token", "")
	if adminToken == "" {
		a.writeError(w, http.StatusServiceUnavailable, "Plex sign-in isn't configured — connect your Plex server in Insights first")
		return
	}
	serverID, err := plex.OwnedServerID(ctx, clientID, adminToken)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, "could not identify your Plex server: "+err.Error())
		return
	}
	ok, err := plex.HasServerAccess(ctx, clientID, token, serverID)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if !ok {
		a.writeError(w, http.StatusForbidden, "Your Plex account doesn't have access to this server.")
		return
	}

	acct, err := plex.GetAccount(ctx, clientID, token)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	// Use the numeric Plex account id — the identifier Overseerr/Tautulli also key on — so imported
	// requests and watch history line up with the account that signs in.
	plexID := strconv.FormatInt(acct.ID, 10)
	if acct.ID == 0 && acct.UUID != "" {
		plexID = acct.UUID
	}
	autoApprove := a.deps.Settings.GetBool(ctx, "plex_login_auto_approve", true)
	u, err := a.deps.Auth.FindOrCreatePlexUser(ctx, plexID, acct.Username, auth.RoleRequester, autoApprove)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not create your account")
		return
	}
	if u.Disabled {
		a.writeError(w, http.StatusForbidden, "This account is disabled.")
		return
	}
	a.startSession(w, r, u, http.StatusOK)
}
