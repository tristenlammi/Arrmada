package httpapi

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/jellyfin"
	"github.com/tristenlammi/arrmada/internal/netutil"
)

// Sign in with Jellyfin: a Jellyfin username and password, or Quick Connect. Either way
// Jellyfin itself says who the person is; Arrmada keeps only the Jellyfin user's id on the
// account. The password passes through once, to Jellyfin, and is never stored, logged or
// echoed; the token Jellyfin hands back is used to log that session out straight away, so
// no "Arrmada" device lingers in Jellyfin's list.

// Settings keys (Settings → Media servers; all admin-only to change).
const (
	keyJellyfinLoginEnabled     = "jellyfin_login_enabled"
	keyJellyfinQuickConnect     = "jellyfin_quickconnect_enabled" // default on (when sign-in is)
	keyJellyfinSignInStaff      = "jellyfin_signin_staff"
	keyJellyfinAutoApproveTypes = "jellyfin_login_auto_approve_types"
)

// jellyfinConnected reports whether a Jellyfin server address is saved: sign-in needs no
// API key, only somewhere to send the person's credentials.
func (a *api) jellyfinConnected(ctx context.Context) bool {
	return a.deps.Jellyfin != nil && a.deps.Jellyfin.URL(ctx) != ""
}

// jellyfinLoginEnabled is whether the login page offers Sign in with Jellyfin.
func (a *api) jellyfinLoginEnabled(ctx context.Context) bool {
	return a.jellyfinConnected(ctx) && a.deps.Settings.GetBool(ctx, keyJellyfinLoginEnabled, false)
}

// jellyfinQuickConnectOn is whether Quick Connect is offered (sign-in and linking): the
// owner's switch, and never for an Emby server, which has no Quick Connect.
func (a *api) jellyfinQuickConnectOn(ctx context.Context) bool {
	if a.deps.Jellyfin != nil && a.deps.Jellyfin.IsEmby(ctx) {
		return false
	}
	return a.jellyfinQuickConnectSetting(ctx)
}

// jellyfinServerKind is "jellyfin" or "emby": which server the Jellyfin slot is set up for.
func (a *api) jellyfinServerKind(ctx context.Context) string {
	if a.deps.Jellyfin == nil {
		return jellyfin.ServerJellyfin
	}
	return a.deps.Jellyfin.ServerKind(ctx)
}

// jfWords puts a sign-in or linking message in the connected server's name: Jellyfin's own
// words, or the same about Emby (without the Quick Connect hint, which Emby can't use).
func (a *api) jfWords(ctx context.Context, msg string) string {
	if a.deps.Jellyfin == nil || !a.deps.Jellyfin.IsEmby(ctx) {
		return msg
	}
	msg = strings.Replace(msg, " No password? Use Quick Connect instead.", "", 1)
	return strings.ReplaceAll(msg, "Jellyfin", "Emby")
}

// jellyfinLoginKind is jellyfinServerKind for a signed-in caller, or while Sign in with
// Jellyfin/Emby is offered; "" for anyone else (a stranger learns nothing about the setup).
func (a *api) jellyfinLoginKind(ctx context.Context, authed bool) string {
	if !authed && !a.jellyfinLoginEnabled(ctx) {
		return ""
	}
	return a.jellyfinServerKind(ctx)
}

// jellyfinQuickConnectSetting is the owner's Quick Connect switch as saved.
func (a *api) jellyfinQuickConnectSetting(ctx context.Context) bool {
	return a.deps.Settings.GetBool(ctx, keyJellyfinQuickConnect, true)
}

// jellyfinAutoApproval is what a new Jellyfin sign-in's requests approve themselves for:
// movies only until the owner picks.
func (a *api) jellyfinAutoApproval(ctx context.Context) auth.AutoApproval {
	if v, ok := a.deps.Settings.Lookup(keyJellyfinAutoApproveTypes); ok {
		return auth.ParseAutoApproval(v)
	}
	return auth.AutoApproval{Movie: true}
}

// jellyfinAuthed is the moment after Jellyfin accepted someone: their session is logged
// out at once (the token isn't needed again, and Jellyfin would otherwise list an
// "Arrmada" device under them), and who they are is all that's kept. A failed logout is
// logged without the token and doesn't stop the sign-in.
func (a *api) jellyfinAuthed(ctx context.Context, c *jellyfin.Client, res jellyfin.AuthResult) jellyfin.User {
	lctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	if err := c.WithToken(res.AccessToken).Logout(lctx); err != nil {
		a.deps.Log.Warn("users: couldn't end the Jellyfin session a sign-in opened", "err", err)
	}
	return res.User
}

// jellyfinFailure answers a Jellyfin sign-in call that failed for a reason other than the
// person's credentials, in plain words. The client never puts what it sent into an error,
// but an odd server's error text could echo it, so anything in secret (the password, the
// Quick Connect secret) is blanked before the error is logged.
func (a *api) jellyfinFailure(w http.ResponseWriter, r *http.Request, err error, secret string) {
	if errors.Is(err, jellyfin.ErrUnreachable) {
		a.writeError(w, http.StatusBadGateway, a.jfWords(r.Context(), "Jellyfin isn't answering right now. Try again in a bit."))
		return
	}
	msg := err.Error()
	if secret != "" {
		msg = strings.ReplaceAll(msg, secret, "«redacted»")
	}
	a.deps.Log.Warn("users: Jellyfin sign-in call failed", "err", msg)
	a.writeError(w, http.StatusBadGateway, a.jfWords(r.Context(), "Jellyfin couldn't sign you in right now. Try again in a bit."))
}

// jellyfinPasswordAuth checks a Jellyfin username and password under the sign-in limits
// and returns the Jellyfin user. On false it has written the answer.
//
// The limits count wrong passwords only: per address (10 in 15 minutes), per username from
// this address (a growing pause after 5), and per username from anywhere outside the LAN
// (the same pause; never applied at home, so a stranger can't lock the owner out). Every
// wrong password also counts against the Jellyfin user inside Jellyfin, which may lock the
// account after a few, so the per-username pause matters more here than for Arrmada's own
// passwords.
func (a *api) jellyfinPasswordAuth(w http.ResponseWriter, r *http.Request, flow, username, password string) (jellyfin.User, bool) {
	ctx := r.Context()
	username = strings.TrimSpace(username)
	if username == "" {
		a.writeError(w, http.StatusBadRequest, a.jfWords(ctx, "Enter your Jellyfin username."))
		return jellyfin.User{}, false
	}
	// A Jellyfin user without a password signs in on Jellyfin's own login page with a
	// blank one. Arrmada may be reachable from places Jellyfin isn't, so a name alone is
	// never enough here: such users can use Quick Connect.
	if password == "" {
		a.writeError(w, http.StatusBadRequest, a.jfWords(ctx, "Enter your Jellyfin password. No password? Use Quick Connect instead."))
		return jellyfin.User{}, false
	}
	ip := netutil.ClientIP(r)
	name := strings.ToLower(username)
	ipKey := "jf" + flow + ":" + ip
	pairKey := "jf" + flow + "-pair:" + name + "|" + ip
	userKey := "jf" + flow + "-user:" + name
	l := a.loginLimiter
	if l != nil {
		if ok, retry := l.blocked(ipKey); ok {
			a.tooManyAttempts(w, retry)
			return jellyfin.User{}, false
		}
		if ok, retry := l.delayed(pairKey); ok {
			a.tooManyAttempts(w, retry)
			return jellyfin.User{}, false
		}
		if a.classifyExternal(r) {
			if ok, retry := l.delayed(userKey); ok {
				a.tooManyAttempts(w, retry)
				return jellyfin.User{}, false
			}
		}
	}
	c := a.deps.Jellyfin.Client(ctx)
	res, err := c.AuthenticateByName(ctx, username, password)
	if errors.Is(err, jellyfin.ErrUnauthorized) {
		if l != nil {
			l.fail(ipKey, name)
			l.fail(pairKey, "")
			if n := l.fail(userKey, name); n >= loginAlertAt && n%loginAlertAt == 0 {
				a.loginFailuresAlert("jellyfin:"+name, n, ip)
			}
		}
		// 401 only on the sign-in route: on a signed-in route the app reads a 401 as its own
		// session having ended.
		code := http.StatusUnauthorized
		if flow != jfFlowLogin {
			code = http.StatusBadRequest
		}
		a.writeError(w, code, a.jfWords(ctx, "Wrong Jellyfin username or password."))
		return jellyfin.User{}, false
	}
	if errors.Is(err, jellyfin.ErrForbidden) {
		// Jellyfin turns a disabled (or locked-out) user away this way.
		a.writeError(w, http.StatusForbidden, a.jfWords(ctx, "Jellyfin won't let this user sign in. They may be disabled in Jellyfin."))
		return jellyfin.User{}, false
	}
	if err != nil {
		a.jellyfinFailure(w, r, err, password)
		return jellyfin.User{}, false
	}
	if l != nil {
		l.reset(pairKey, "")
		l.reset(userKey, "")
		l.reset(ipKey, name)
	}
	return a.jellyfinAuthed(ctx, c, res), true
}

// jellyfinQCStart starts a Quick Connect request for flow and answers {id, code}.
func (a *api) jellyfinQCStart(w http.ResponseWriter, r *http.Request, flow string, userID int64) {
	ctx := r.Context()
	st, err := a.deps.Jellyfin.Client(ctx).QuickConnectInitiate(ctx)
	if errors.Is(err, jellyfin.ErrUnauthorized) || errors.Is(err, jellyfin.ErrForbidden) {
		a.writeError(w, http.StatusConflict, a.jfWords(ctx, "Quick Connect is switched off in Jellyfin (Dashboard → General)."))
		return
	}
	if err != nil {
		a.jellyfinFailure(w, r, err, "")
		return
	}
	if st.Secret == "" || st.Code == "" {
		a.writeError(w, http.StatusBadGateway, a.jfWords(ctx, "Jellyfin didn't give a Quick Connect code. Try again."))
		return
	}
	id, err := a.bindJFQC(w, r, flow, userID, st.Secret)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not start Quick Connect")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"id": id, "code": st.Code})
}

// jellyfinQCPoll asks Jellyfin whether a Quick Connect code was approved. Until then it
// answers {pending:true} and returns false; once approved, the secret is claimed (exactly
// one poll gets it), traded for a sign-in and forgotten, and the Jellyfin user returned.
func (a *api) jellyfinQCPoll(w http.ResponseWriter, r *http.Request, flow string, userID int64) (jellyfin.User, bool) {
	ctx := r.Context()
	id := r.PathValue("id")
	secret := a.jfQCSecret(r, flow, userID, id)
	if secret == "" {
		a.writeError(w, http.StatusForbidden, errJFQCElsewhere)
		return jellyfin.User{}, false
	}
	c := a.deps.Jellyfin.Client(ctx)
	st, err := c.QuickConnectConnect(ctx, secret)
	if errors.Is(err, jellyfin.ErrNotFound) {
		a.jfQC.forget(id)
		a.writeError(w, http.StatusGone, "This Quick Connect code has expired. Start again.")
		return jellyfin.User{}, false
	}
	if err != nil {
		a.jellyfinFailure(w, r, err, secret)
		return jellyfin.User{}, false
	}
	if !st.Authenticated {
		a.writeJSON(w, http.StatusOK, map[string]any{"pending": true})
		return jellyfin.User{}, false
	}
	// Approved. Jellyfin would keep signing this secret in until it expires, so it is
	// taken out of the table before it's used: a second poll finds nothing.
	if !a.jfQC.claim(id) {
		a.writeError(w, http.StatusGone, "This Quick Connect code was already used. Start again.")
		return jellyfin.User{}, false
	}
	res, err := c.AuthenticateWithQuickConnect(ctx, secret)
	if err != nil {
		a.jellyfinFailure(w, r, err, secret)
		return jellyfin.User{}, false
	}
	return a.jellyfinAuthed(ctx, c, res), true
}

// handleJellyfinLogin: POST /api/v1/auth/jellyfin {username, password}.
func (a *api) handleJellyfinLogin(w http.ResponseWriter, r *http.Request) {
	if !a.jellyfinLoginEnabled(r.Context()) {
		a.writeError(w, http.StatusForbidden, a.jfWords(r.Context(), "Jellyfin sign-in is off."))
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !a.decodeJSON(w, r, &body) {
		return
	}
	ju, ok := a.jellyfinPasswordAuth(w, r, jfFlowLogin, body.Username, body.Password)
	if !ok {
		return
	}
	a.finishJellyfinSignIn(w, r, ju)
}

// handleJellyfinQCStart: POST /api/v1/auth/jellyfin/quickconnect → {id, code}.
func (a *api) handleJellyfinQCStart(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !a.jellyfinLoginEnabled(ctx) || !a.jellyfinQuickConnectOn(ctx) {
		a.writeError(w, http.StatusForbidden, "Quick Connect sign-in is off.")
		return
	}
	// Each start asks Jellyfin for a code: throttled so the open endpoint can't flood it.
	if !a.loginAllowed(w, r, "jfqc:"+netutil.ClientIP(r)) {
		return
	}
	a.jellyfinQCStart(w, r, jfFlowLogin, 0)
}

// handleJellyfinQCPoll: GET /api/v1/auth/jellyfin/quickconnect/{id} → {pending:true}, or a
// session once the code was approved.
func (a *api) handleJellyfinQCPoll(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	if !a.jellyfinLoginEnabled(ctx) || !a.jellyfinQuickConnectOn(ctx) {
		a.writeError(w, http.StatusForbidden, "Quick Connect sign-in is off.")
		return
	}
	ju, ok := a.jellyfinQCPoll(w, r, jfFlowLogin, 0)
	if !ok {
		return
	}
	a.finishJellyfinSignIn(w, r, ju)
}

// finishJellyfinSignIn turns a Jellyfin user Jellyfin has just vouched for into an Arrmada
// session: the account linked to them, or a new requester. Refused: a Jellyfin user
// disabled in Jellyfin or blocked here, a disabled account, and a staff account unless the
// owner allowed staff Jellyfin sign-in. A Jellyfin administrator is never made a new
// requester (their requests and phones would split from their real account): they link
// Jellyfin from their own account first.
func (a *api) finishJellyfinSignIn(w http.ResponseWriter, r *http.Request, ju jellyfin.User) {
	ctx := r.Context()
	if ju.ID == "" {
		a.writeError(w, http.StatusBadGateway, a.jfWords(ctx, "Jellyfin didn't say who you are. Try again."))
		return
	}
	if ok, msg := a.jellyfinSignInAllowed(ctx, ju.ID, nil); !ok {
		a.writeError(w, http.StatusForbidden, msg)
		return
	}
	if ju.Policy.IsDisabled {
		a.writeError(w, http.StatusForbidden, a.jfWords(ctx, "This Jellyfin user is disabled in Jellyfin."))
		return
	}
	u, err := a.deps.Auth.UserByJellyfinID(ctx, ju.ID)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not look up your account")
		return
	}
	if u == nil {
		if ju.Policy.IsAdministrator {
			a.writeError(w, http.StatusConflict, a.jfWords(ctx, "This is a Jellyfin administrator. Sign in with your password, then link Jellyfin from your account page."))
			return
		}
		if u, err = a.deps.Auth.FindOrCreateJellyfinUser(ctx, ju.ID, ju.Name, auth.RoleRequester, a.jellyfinAutoApproval(ctx)); err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not create your account")
			return
		}
		a.deps.Log.Info("users: signed in with Jellyfin", "user_id", u.ID)
	}
	if ok, msg := a.jellyfinSignInAllowed(ctx, ju.ID, u); !ok {
		a.writeError(w, http.StatusForbidden, msg)
		return
	}
	if u.Role.AtLeast(auth.RoleManager) {
		if !a.deps.Settings.GetBool(ctx, keyJellyfinSignInStaff, false) {
			a.deps.Log.Warn("users: Jellyfin sign-in into a staff account refused", "user_id", u.ID)
			a.writeError(w, http.StatusForbidden, a.jfWords(ctx, "This Jellyfin user is linked to a staff account. Sign in with your password."))
			return
		}
		a.deps.Log.Info("users: staff signed in with Jellyfin", "user_id", u.ID)
	}
	a.finishExternalSignIn(w, r, u)
}

// --- your own Jellyfin link ---

// handleMyJellyfin: GET /api/v1/me/jellyfin → the link, and whether linking is possible
// (a Jellyfin server is connected) and Quick Connect offered.
func (a *api) handleMyJellyfin(w http.ResponseWriter, r *http.Request) {
	me, _ := userFrom(r)
	if me == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	ctx := r.Context()
	l, err := a.deps.Auth.JellyfinLinkFor(ctx, me.ID)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, a.jfWords(ctx, "could not read your Jellyfin link"))
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"linked": l.Linked, "jellyfin_username": l.JellyfinUsername, "can_unlink": l.CanUnlink,
		"available":    a.jellyfinConnected(ctx),
		"quickconnect": a.jellyfinConnected(ctx) && a.jellyfinQuickConnectOn(ctx),
	})
}

// handleJellyfinLink: POST /api/v1/me/jellyfin/link {username, password} links the
// Jellyfin user those sign in as.
func (a *api) handleJellyfinLink(w http.ResponseWriter, r *http.Request) {
	me, _ := userFrom(r)
	if me == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	if !a.jellyfinConnected(r.Context()) {
		a.writeError(w, http.StatusServiceUnavailable, a.jfWords(r.Context(), "Jellyfin isn't connected."))
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !a.decodeJSON(w, r, &body) {
		return
	}
	ju, ok := a.jellyfinPasswordAuth(w, r, jfFlowLink, body.Username, body.Password)
	if !ok {
		return
	}
	a.linkJellyfinTo(w, r, me, ju)
}

// handleJellyfinLinkQCStart: POST /api/v1/me/jellyfin/link/quickconnect → {id, code}.
func (a *api) handleJellyfinLinkQCStart(w http.ResponseWriter, r *http.Request) {
	me, _ := userFrom(r)
	if me == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	ctx := r.Context()
	if !a.jellyfinConnected(ctx) || !a.jellyfinQuickConnectOn(ctx) {
		a.writeError(w, http.StatusForbidden, "Quick Connect is off.")
		return
	}
	if !a.loginAllowed(w, r, "jfqclink:"+strconv.FormatInt(me.ID, 10)) {
		return
	}
	a.jellyfinQCStart(w, r, jfFlowLink, me.ID)
}

// handleJellyfinLinkQCPoll: GET /api/v1/me/jellyfin/link/quickconnect/{id}.
func (a *api) handleJellyfinLinkQCPoll(w http.ResponseWriter, r *http.Request) {
	me, _ := userFrom(r)
	if me == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	ctx := r.Context()
	if !a.jellyfinConnected(ctx) || !a.jellyfinQuickConnectOn(ctx) {
		a.writeError(w, http.StatusForbidden, "Quick Connect is off.")
		return
	}
	ju, ok := a.jellyfinQCPoll(w, r, jfFlowLink, me.ID)
	if !ok {
		return
	}
	a.linkJellyfinTo(w, r, me, ju)
}

// linkJellyfinTo links ju to the signed-in account: {linked, jellyfin_username}, or 409
// when that Jellyfin user belongs to another account (an admin learns its id to merge,
// unless it's a staff account).
func (a *api) linkJellyfinTo(w http.ResponseWriter, r *http.Request, me *auth.User, ju jellyfin.User) {
	err := a.deps.Auth.LinkJellyfin(r.Context(), me.ID, ju.ID, ju.Name)
	var taken *auth.ErrJellyfinAlreadyLinked
	if errors.As(err, &taken) {
		body := map[string]any{
			"status":            "error",
			"message":           a.jfWords(r.Context(), "This Jellyfin user is already linked to ") + taken.Username + ".",
			"already_linked_to": taken.Username,
		}
		if me.Role == auth.RoleAdmin && !taken.Role.AtLeast(auth.RoleManager) {
			body["user_id"] = taken.UserID
			body["mergeable"] = true
		}
		a.writeJSON(w, http.StatusConflict, body)
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, a.jfWords(r.Context(), "could not link your Jellyfin user"))
		return
	}
	a.deps.Log.Info("users: Jellyfin user linked", "user_id", me.ID)
	a.writeJSON(w, http.StatusOK, map[string]any{"linked": true, "jellyfin_username": ju.Name})
}

// handleJellyfinUnlink: DELETE /api/v1/me/jellyfin/link, unless it's the account's last
// way in.
func (a *api) handleJellyfinUnlink(w http.ResponseWriter, r *http.Request) {
	me, _ := userFrom(r)
	if me == nil {
		a.writeError(w, http.StatusUnauthorized, "sign in first")
		return
	}
	err := a.deps.Auth.UnlinkJellyfin(r.Context(), me.ID, true)
	if errors.Is(err, auth.ErrJellyfinOnlyLogin) {
		a.writeError(w, http.StatusConflict, a.jfWords(r.Context(), "You sign in only with Jellyfin, so unlinking it would lock you out. Ask an admin to set a password for your account first."))
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, a.jfWords(r.Context(), "could not unlink Jellyfin"))
		return
	}
	a.deps.Log.Info("users: Jellyfin user unlinked", "user_id", me.ID)
	a.writeJSON(w, http.StatusOK, auth.JellyfinLink{})
}
