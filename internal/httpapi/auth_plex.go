package httpapi

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strconv"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/netutil"
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

// plexServerID is the machine id of the server Plex sign-ins are checked against: the
// connected server's own id when Settings → Plex has stored it, else the first server the
// owner's token owns (an owner with two servers is then gated on the right one).
func (a *api) plexServerID(ctx context.Context, clientID, adminToken string) (string, error) {
	if id := a.deps.Settings.Get(ctx, insights.KeyMachineID, ""); id != "" {
		return id, nil
	}
	return plex.OwnedServerID(ctx, clientID, adminToken)
}

// handlePlexLoginStart begins a Plex sign-in: returns a PIN id + the plex.tv URL to authorize it.
func (a *api) handlePlexLoginStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !a.deps.Settings.GetBool(ctx, "plex_login_enabled", false) {
		a.writeError(w, http.StatusForbidden, "Plex sign-in is disabled")
		return
	}
	// Each pin start hits plex.tv; throttle so the unauthenticated endpoint can't
	// be used to amplify traffic at plex.tv or spin up pins without bound.
	if !a.loginAllowed(w, r, "plexpin:"+netutil.ClientIP(r)) {
		return
	}
	// ?mode=redirect: no popup could be opened (iPhone, the installed app, a blocker), so
	// plex.tv sends this browser back to the login page with the PIN once approved.
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
		forward, _ = plexForwardURL(r, "/?plexpin="+strconv.Itoa(pin.ID))
	}
	if err := a.bindPlexPin(w, r, plexFlowLogin, 0, pin.ID); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not start Plex sign-in")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"id":       pin.ID,
		"auth_url": plex.AuthURL(clientID, pin.Code, plexLoginProduct, forward),
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
	// Only the browser that started this PIN may collect it (see plexpin.go).
	if !a.plexPinOwned(r, plexFlowLogin, 0, pinID) {
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
	// Approved: whatever the outcome below, this PIN is spent.
	defer a.plexPins.forget(pinID)

	// Authorization gate: the signing-in user must have access to THIS Plex server.
	adminToken := a.deps.Settings.Get(ctx, "insights_plex_token", "")
	if adminToken == "" {
		a.writeError(w, http.StatusServiceUnavailable, "Plex sign-in isn't configured — connect your Plex server in Settings → Plex first")
		return
	}
	serverID, err := a.plexServerID(ctx, clientID, adminToken)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, "could not identify your Plex server: "+err.Error())
		return
	}
	access, owner, err := plex.ServerAccess(ctx, clientID, token, serverID)
	if err != nil {
		a.writeError(w, http.StatusBadGateway, err.Error())
		return
	}
	if !access {
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
	plexID := plexAccountID(acct)
	// A blocked Plex account is turned away before an account is found or made for it.
	if ok, msg := a.plexSignInAllowed(ctx, plexID, nil); !ok {
		a.writeError(w, http.StatusForbidden, msg)
		return
	}
	staffAllowed := a.deps.Settings.GetBool(ctx, keyPlexSignInStaff, false)

	var u *auth.User
	if owner {
		// The server's owner is never made a requester: their requests, inbox and phones
		// would split from their real account. Their own linked account, or (only with
		// staff Plex sign-in on) the one admin there is, or instructions.
		if u, err = a.deps.Auth.UserByPlexID(ctx, plexID); err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not look up your account")
			return
		}
		if u == nil {
			if u, err = a.linkOwnerToAdmin(ctx, staffAllowed, plexID, acct.Username); err != nil {
				a.writeError(w, http.StatusInternalServerError, "could not link your Plex account")
				return
			}
		}
		if u == nil {
			a.writeError(w, http.StatusConflict, "This Plex account owns the server — sign in with your password and link Plex from your account menu.")
			return
		}
	} else {
		autoApprove := a.plexAutoApproval(ctx) // per type; movies only unless the owner chose
		if u, err = a.deps.Auth.FindOrCreatePlexUser(ctx, plexID, acct.Username, auth.RoleRequester, autoApprove); err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not create your account")
			return
		}
	}
	if ok, msg := a.plexSignInAllowed(ctx, plexID, u); !ok {
		a.writeError(w, http.StatusForbidden, msg)
		return
	}
	// A Plex account linked to an admin or manager opens that account only when the owner
	// has said so (Settings → Plex): otherwise anyone who gets hold of the Plex account
	// would have the staff console.
	if u.Role.AtLeast(auth.RoleManager) && !staffAllowed {
		a.deps.Log.Warn("users: Plex sign-in into a staff account refused", "user_id", u.ID)
		a.writeError(w, http.StatusForbidden, "This Plex account is linked to a staff account — sign in with your password.")
		return
	}
	if u.Role.AtLeast(auth.RoleManager) {
		a.deps.Log.Info("users: staff signed in with Plex", "user_id", u.ID)
	}
	a.startSession(w, r, u, http.StatusOK)
}

// keyPlexSignInStaff (Settings → Plex, admin, default off): whether Sign in with Plex may
// open an admin or manager account linked to that Plex account.
const keyPlexSignInStaff = "plex_signin_staff"

// linkOwnerToAdmin is the server owner's first Plex sign-in with staff Plex sign-in on:
// when there is exactly one admin account, it's enabled and has no Plex link, it can only
// be the owner's, so the Plex account is linked to it. Anything else — the setting off,
// several admins, an admin already linked to some other Plex account — returns nil and
// the owner links Plex by hand from their account menu.
func (a *api) linkOwnerToAdmin(ctx context.Context, staffAllowed bool, plexID, plexUsername string) (*auth.User, error) {
	if !staffAllowed {
		return nil, nil
	}
	users, err := a.deps.Auth.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	var only *auth.User
	for i := range users {
		if users[i].Role != auth.RoleAdmin {
			continue
		}
		if only != nil {
			return nil, nil // more than one admin: can't tell which is the owner
		}
		only = &users[i]
	}
	if only == nil || only.Disabled || only.PlexLinked {
		return nil, nil
	}
	if err := a.deps.Auth.LinkPlex(ctx, only.ID, plexID, plexUsername); err != nil {
		var taken *auth.ErrPlexAlreadyLinked
		if errors.As(err, &taken) {
			return nil, nil // linked meanwhile; the next sign-in finds it
		}
		return nil, err
	}
	a.deps.Log.Info("users: the server owner's Plex account was linked to the admin account", "user_id", only.ID)
	return a.deps.Auth.UserByID(ctx, only.ID)
}
