package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
)

// Deleting a Plex user doesn't keep them out: their next "Sign in with Plex" would simply
// create a new account. The block list stops that. It's a settings value holding the Plex
// account ids the owner has blocked, with the display name they already saw, and nothing
// else about the person.

const plexBlocksKey = "plex_login_blocked"

type plexBlock struct {
	PlexID string `json:"plex_id"`
	Name   string `json:"name"`
	At     string `json:"at"`
}

// plexBlocksMu serialises the read-modify-write of the block list.
var plexBlocksMu sync.Mutex

func (a *api) plexBlocks(ctx context.Context) []plexBlock {
	var out []plexBlock
	if raw := a.deps.Settings.Get(ctx, plexBlocksKey, ""); raw != "" {
		if err := json.Unmarshal([]byte(raw), &out); err != nil {
			a.deps.Log.Warn("users: the blocked Plex accounts list is unreadable", "err", err)
		}
	}
	return out
}

func (a *api) savePlexBlocks(ctx context.Context, list []plexBlock) error {
	if list == nil {
		list = []plexBlock{}
	}
	b, err := json.Marshal(list)
	if err != nil {
		return err
	}
	return a.deps.Settings.Set(ctx, plexBlocksKey, string(b))
}

// plexBlocked reports whether a Plex account id is on the block list.
func (a *api) plexBlocked(ctx context.Context, plexID string) bool {
	plexID = strings.TrimSpace(plexID)
	if plexID == "" {
		return false
	}
	for _, b := range a.plexBlocks(ctx) {
		if b.PlexID == plexID {
			return true
		}
	}
	return false
}

// addPlexBlock blocks a Plex account id. added is false when it was already blocked.
func (a *api) addPlexBlock(ctx context.Context, plexID, name string) (added bool, err error) {
	plexBlocksMu.Lock()
	defer plexBlocksMu.Unlock()
	list := a.plexBlocks(ctx)
	for _, b := range list {
		if b.PlexID == plexID {
			return false, nil
		}
	}
	list = append(list, plexBlock{PlexID: plexID, Name: name, At: time.Now().UTC().Format(time.RFC3339)})
	return true, a.savePlexBlocks(ctx, list)
}

// removePlexBlock unblocks a Plex account id. removed is false when it wasn't blocked.
func (a *api) removePlexBlock(ctx context.Context, plexID string) (removed bool, err error) {
	plexBlocksMu.Lock()
	defer plexBlocksMu.Unlock()
	list := a.plexBlocks(ctx)
	kept := list[:0]
	for _, b := range list {
		if b.PlexID == plexID {
			removed = true
			continue
		}
		kept = append(kept, b)
	}
	if !removed {
		return false, nil
	}
	return true, a.savePlexBlocks(ctx, kept)
}

// plexSignInAllowed is the Plex sign-in decision: a blocked Plex account is refused before
// any account is found or made for it (u is nil then), and a disabled account after.
func (a *api) plexSignInAllowed(ctx context.Context, plexID string, u *auth.User) (bool, string) {
	if a.plexBlocked(ctx, plexID) {
		return false, "This Plex account isn't allowed to sign in here."
	}
	if u != nil && u.Disabled {
		return false, "This account is disabled."
	}
	return true, ""
}

// handleListPlexBlocks lists the blocked Plex accounts.
func (a *api) handleListPlexBlocks(w http.ResponseWriter, r *http.Request) {
	list := a.plexBlocks(r.Context())
	if list == nil {
		list = []plexBlock{}
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"blocks": list})
}

// handleBlockUserPlex blocks the Plex account linked to a user, so it can't sign in here
// again. Their existing account is left as it is; to sign them out now, disable it.
func (a *api) handleBlockUserPlex(w http.ResponseWriter, r *http.Request) {
	id, ok := a.pathID(w, r)
	if !ok {
		return
	}
	if code, msg := a.blockUserPlex(r.Context(), r, id); code != 0 {
		a.writeError(w, code, msg)
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"blocks": a.plexBlocks(r.Context())})
}

// blockUserPlex adds the user's Plex id to the block list. It returns an HTTP status and
// message when it can't, or 0 on success.
func (a *api) blockUserPlex(ctx context.Context, r *http.Request, id int64) (int, string) {
	u, err := a.deps.Auth.UserByID(ctx, id)
	if err != nil {
		return http.StatusNotFound, "user not found"
	}
	if me, ok := userFrom(r); ok && me.ID == id {
		return http.StatusBadRequest, "you can't block your own Plex account"
	}
	plexID := a.deps.Auth.PlexIDForUser(ctx, id)
	if plexID == "" {
		return http.StatusBadRequest, "this account isn't linked to Plex"
	}
	if _, err := a.addPlexBlock(ctx, plexID, u.Username); err != nil {
		return http.StatusInternalServerError, "could not save the block"
	}
	a.deps.Log.Info("users: Plex account blocked", "user_id", id)
	return 0, ""
}

// handleUnblockPlex removes a Plex account from the block list.
func (a *api) handleUnblockPlex(w http.ResponseWriter, r *http.Request) {
	removed, err := a.removePlexBlock(r.Context(), r.PathValue("plexID"))
	if err != nil {
		a.writeError(w, http.StatusInternalServerError, "could not save the block list")
		return
	}
	if !removed {
		a.writeError(w, http.StatusNotFound, "that Plex account isn't blocked")
		return
	}
	a.writeJSON(w, http.StatusOK, map[string]any{"blocks": a.plexBlocks(r.Context())})
}
