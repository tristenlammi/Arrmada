package httpapi

import (
	"errors"
	"net/http"
	"strings"

	"github.com/tristenlammi/arrmada/internal/auth"
)

func (a *api) handleListUsers(w http.ResponseWriter, r *http.Request) {
	users, err := a.deps.Auth.ListUsers(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not list users")
		return
	}
	// Plex-linked users also say whether their Plex account is blocked, so the page can
	// offer Block or show that it's done.
	type row struct {
		auth.User
		PlexBlocked bool `json:"plex_blocked,omitempty"`
	}
	blocked := map[string]bool{}
	for _, b := range a.plexBlocks(r.Context()) {
		blocked[b.PlexID] = true
	}
	out := make([]row, 0, len(users))
	for _, u := range users {
		rw := row{User: u}
		if u.PlexLinked && len(blocked) > 0 {
			rw.PlexBlocked = blocked[a.deps.Auth.PlexIDForUser(r.Context(), u.ID)]
		}
		out = append(out, rw)
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"users": out})
}

// handleCreateUser adds an account by email + password. Role defaults to requester
// (a normal user who can request media but not administer). auto_approve makes that
// user's requests skip the approval queue.
func (a *api) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		Role        string `json:"role"`
		AutoApprove bool   `json:"auto_approve"` // legacy: every type
		autoApprovalFields
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	email := strings.TrimSpace(req.Email)
	if !strings.Contains(email, "@") || strings.HasPrefix(email, "@") || strings.HasSuffix(email, "@") {
		a.writeError(w, http.StatusBadRequest, "a valid email is required")
		return
	}
	role := auth.Role(req.Role)
	if !auth.ValidRole(role) {
		role = auth.RoleRequester
	}
	// CreateUser lowercases the email and refuses one that differs from an existing account
	// only by case; the client just trims, so the two can't disagree.
	aa := req.autoApprovalFields.apply(auth.AllTypes(req.AutoApprove))
	u, err := a.deps.Auth.CreateUser(r.Context(), email, req.Password, role, aa.All())
	if errors.Is(err, auth.ErrUserExists) {
		a.writeError(w, http.StatusConflict, "an account with that email already exists")
		return
	}
	if errors.Is(err, auth.ErrWeakPassword) {
		a.writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if aa != auth.AllTypes(aa.All()) {
		// Some types but not all: CreateUser takes the all-or-nothing flag.
		if err := a.deps.Auth.UpdateUser(r.Context(), u.ID, u.Role, aa); err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not set auto-approve")
			return
		}
		u.AutoApprove, u.AutoApproveMovie, u.AutoApproveSeries, u.AutoApproveBook = aa.All(), aa.Movie, aa.Series, aa.Book
	}
	a.writeJSON(w, http.StatusCreated, u)
}

// enabledAdmins counts the admins who can still sign in. A disabled admin can't rescue
// the instance, so the last-admin guards count only these.
func enabledAdmins(users []auth.User) int {
	n := 0
	for _, u := range users {
		if u.Role == auth.RoleAdmin && !u.Disabled {
			n++
		}
	}
	return n
}

// handleUpdateUser edits an existing account: role, auto-approve, whether they can sign
// in, and optionally a new password. Only the provided fields change.
func (a *api) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Role        *string `json:"role"`
		AutoApprove *bool   `json:"auto_approve"` // legacy: every type
		Password    *string `json:"password"`
		autoApprovalFields
		Disabled *bool `json:"disabled"`
	}
	if !a.decodeJSON(w, r, &req) {
		return
	}
	users, err := a.deps.Auth.ListUsers(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load users")
		return
	}
	var cur *auth.User
	for i := range users {
		if users[i].ID == id {
			cur = &users[i]
		}
	}
	if cur == nil {
		a.writeError(w, http.StatusNotFound, "user not found")
		return
	}

	role := cur.Role
	if req.Role != nil {
		nr := auth.Role(*req.Role)
		if !auth.ValidRole(nr) {
			a.writeError(w, http.StatusBadRequest, "invalid role")
			return
		}
		role = nr
	}
	disabled := cur.Disabled
	if req.Disabled != nil {
		disabled = *req.Disabled
	}
	if me, ok := userFrom(r); ok && me.ID == id && disabled && !cur.Disabled {
		a.writeError(w, http.StatusBadRequest, "you can't turn off your own sign-in")
		return
	}
	// Don't leave the instance with no admin who can sign in, nor with no admin row at all
	// (that would reopen first-run setup).
	adminRows := 0
	for _, u := range users {
		if u.Role == auth.RoleAdmin {
			adminRows++
		}
	}
	demoted := cur.Role == auth.RoleAdmin && role != auth.RoleAdmin
	if (demoted && adminRows <= 1) ||
		(cur.Role == auth.RoleAdmin && !cur.Disabled && (demoted || disabled) && enabledAdmins(users) <= 1) {
		if demoted {
			a.writeError(w, http.StatusBadRequest, "can't demote the last admin")
		} else {
			a.writeError(w, http.StatusBadRequest, "can't turn off sign-in for the last admin")
		}
		return
	}
	autoApprove := cur.AutoApproval()
	if req.AutoApprove != nil {
		autoApprove = auth.AllTypes(*req.AutoApprove)
	}
	autoApprove = req.autoApprovalFields.apply(autoApprove)
	if err := a.deps.Auth.UpdateUser(r.Context(), id, role, autoApprove); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update user")
		return
	}
	if disabled != cur.Disabled {
		if err := a.deps.Auth.SetDisabled(r.Context(), id, disabled); err != nil {
			a.writeError(w, http.StatusInternalServerError, "could not change whether they can sign in")
			return
		}
		a.deps.Log.Info("users: sign-in changed", "user_id", id, "disabled", disabled)
	}
	if req.Password != nil && *req.Password != "" {
		if err := a.deps.Auth.SetPassword(r.Context(), id, *req.Password); err != nil {
			if errors.Is(err, auth.ErrWeakPassword) {
				a.writeError(w, http.StatusBadRequest, "password must be at least 8 characters")
				return
			}
			a.writeError(w, http.StatusInternalServerError, "could not update password")
			return
		}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "role": role, "disabled": disabled, "auto_approve": autoApprove.All(),
		"auto_approve_movie": autoApprove.Movie, "auto_approve_series": autoApprove.Series, "auto_approve_book": autoApprove.Book,
	})
}

// handleUserImpact reports, as counts only, what deleting a user would take with them.
// It never names a book: admins see how much someone listened, never what.
func (a *api) handleUserImpact(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	imp, err := a.deps.Auth.DeletionImpact(r.Context(), id)
	if errors.Is(err, auth.ErrNotFound) {
		a.writeError(w, http.StatusNotFound, "user not found")
		return
	}
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not count this user's data")
		return
	}
	a.writeJSON(w, http.StatusOK, imp)
}

// handleDeleteUser removes an account. When the account holds listening data the request
// must carry ?confirm=<username>, and the database is copied first either way.
func (a *api) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if me, ok := userFrom(r); ok && me.ID == id {
		a.writeError(w, http.StatusBadRequest, "you can't delete your own account")
		return
	}
	// Don't strand the instance with no admin.
	users, err := a.deps.Auth.ListUsers(r.Context())
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not load users")
		return
	}
	admins := 0
	var target *auth.User
	for i := range users {
		if users[i].Role == auth.RoleAdmin {
			admins++
		}
		if users[i].ID == id {
			target = &users[i]
		}
	}
	if target == nil {
		a.writeError(w, http.StatusNotFound, "user not found")
		return
	}
	// The last admin row also keeps first-run setup shut, so it stays even when disabled.
	if target.Role == auth.RoleAdmin && (admins <= 1 || (!target.Disabled && enabledAdmins(users) <= 1)) {
		a.writeError(w, http.StatusBadRequest, "can't delete the last admin account")
		return
	}
	// ?block_plex=1 also blocks their Plex account, so deleting them sticks: otherwise their
	// next "Sign in with Plex" would just make a new account.
	var blockPlexID string
	if r.URL.Query().Get("block_plex") == "1" {
		if blockPlexID = a.deps.Auth.PlexIDForUser(r.Context(), id); blockPlexID == "" {
			a.writeError(w, http.StatusBadRequest, "this account isn't linked to Plex, so there's nothing to block")
			return
		}
	}
	// Deleting someone erases their audiobook places and listening history with them. The
	// dialog asks for the username in that case; the API asks too, so a script or a stray
	// request can't skip it.
	imp, err := a.deps.Auth.DeletionImpact(r.Context(), id)
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not check what deleting this user would remove")
		return
	}
	if imp.HasListeningData() && r.URL.Query().Get("confirm") != target.Username {
		a.writeError(w, http.StatusBadRequest, "this user has audiobook places and listening history — type their username to confirm the delete")
		return
	}
	// A copy of the database first, so a mistaken delete can be undone from a backup.
	if a.deps.Snapshot == nil {
		a.writeError(w, http.StatusInternalServerError, "couldn't take a safety copy first — nothing was deleted")
		return
	}
	// Accounts with nothing to lose get a kind of their own. Only the newest few copies of
	// each kind are kept, so clearing out a few empty test or guest accounts must not prune
	// the one copy that still holds a family member's audiobook places.
	kind := "pre-delete-user"
	if !imp.HasListeningData() && imp.Bookmarks == 0 {
		kind = "pre-delete-empty-user"
	}
	path, err := a.deps.Snapshot(r.Context(), kind)
	if err != nil {
		a.deps.Log.Error("users: safety copy before delete failed", "user_id", id, "err", err)
		a.writeError(w, http.StatusInternalServerError, "couldn't take a safety copy first — nothing was deleted ("+err.Error()+")")
		return
	}
	a.deps.Log.Info("users: database copied before deleting a user", "user_id", id, "backup", path)
	// Block first, so there's no moment where they're gone but could sign straight back in.
	var blockAdded bool
	if blockPlexID != "" {
		if blockAdded, err = a.addPlexBlock(r.Context(), blockPlexID, target.Username); err != nil {
			a.writeError(w, http.StatusInternalServerError, "couldn't block their Plex account — nothing was deleted")
			return
		}
		a.deps.Log.Info("users: Plex account blocked", "user_id", id)
	}
	if err := a.deps.Auth.DeleteUser(r.Context(), id); err != nil {
		// The delete didn't happen, so don't leave behind a block that was only its half.
		if blockAdded {
			_, _ = a.removePlexBlock(r.Context(), blockPlexID)
		}
		if errors.Is(err, auth.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "user not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not delete user")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
