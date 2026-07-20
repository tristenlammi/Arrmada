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
	if users == nil {
		users = []auth.User{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"users": users})
}

// handleCreateUser adds an account by email + password. Role defaults to requester
// (a normal user who can request media but not administer). auto_approve makes that
// user's requests skip the approval queue.
func (a *api) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Email       string `json:"email"`
		Password    string `json:"password"`
		Role        string `json:"role"`
		AutoApprove bool   `json:"auto_approve"`
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
	u, err := a.deps.Auth.CreateUser(r.Context(), email, req.Password, role, req.AutoApprove)
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
	a.writeJSON(w, http.StatusCreated, u)
}

// handleUpdateUser edits an existing account: role, auto-approve, and optionally a new
// password. Only the provided fields change.
func (a *api) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	var req struct {
		Role        *string `json:"role"`
		AutoApprove *bool   `json:"auto_approve"`
		Password    *string `json:"password"`
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
	admins := 0
	for i := range users {
		if users[i].Role == auth.RoleAdmin {
			admins++
		}
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
		// Don't leave the instance with no admin.
		if cur.Role == auth.RoleAdmin && nr != auth.RoleAdmin && admins <= 1 {
			a.writeError(w, http.StatusBadRequest, "can't demote the last admin")
			return
		}
		role = nr
	}
	autoApprove := cur.AutoApprove
	if req.AutoApprove != nil {
		autoApprove = *req.AutoApprove
	}
	if err := a.deps.Auth.UpdateUser(r.Context(), id, role, autoApprove); err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not update user")
		return
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
	a.writeJSON(w, http.StatusOK, map[string]any{"id": id, "role": role, "auto_approve": autoApprove})
}

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
	users, _ := a.deps.Auth.ListUsers(r.Context())
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
	if target != nil && target.Role == auth.RoleAdmin && admins <= 1 {
		a.writeError(w, http.StatusBadRequest, "can't delete the last admin account")
		return
	}
	if err := a.deps.Auth.DeleteUser(r.Context(), id); err != nil {
		if errors.Is(err, auth.ErrNotFound) {
			a.writeError(w, http.StatusNotFound, "user not found")
			return
		}
		a.writeError(w, http.StatusInternalServerError, "could not delete user")
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
