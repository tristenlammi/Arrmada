package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
)

// The server normalises the email, so a capitalised one is stored lowercased and a
// second spelling of the same address is a 409.
func TestCreateUserNormalisesEmail(t *testing.T) {
	s := newRouteServer(t, nil)
	_, admin := s.user(t, "owner@example.com", auth.RoleAdmin)

	rec := s.doBody("POST", "/api/v1/users", `{"email":" Mum@Gmail.com ","password":"supersecret","role":"readonly"}`, admin)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create: HTTP %d %s", rec.Code, rec.Body)
	}
	var u auth.User
	_ = json.Unmarshal(rec.Body.Bytes(), &u)
	if u.Username != "mum@gmail.com" || u.Role != auth.RoleReadonly {
		t.Errorf("created %+v, want mum@gmail.com as readonly", u)
	}
	rec = s.doBody("POST", "/api/v1/users", `{"email":"MUM@gmail.com","password":"supersecret"}`, admin)
	if rec.Code != http.StatusConflict {
		t.Errorf("case duplicate: HTTP %d %s, want 409", rec.Code, rec.Body)
	}
}

// Turning off sign-in takes effect on the user's very next request.
func TestDisableUserSignsThemOut(t *testing.T) {
	s := newRouteServer(t, nil)
	_, admin := s.user(t, "owner@example.com", auth.RoleAdmin)
	kid, kidCookie := s.user(t, "kid@example.com", auth.RoleRequester)

	if rec := s.do("GET", "/api/v1/auth/me", kidCookie); rec.Code != http.StatusOK {
		t.Fatalf("before: HTTP %d", rec.Code)
	}
	rec := s.doBody("PUT", fmt.Sprintf("/api/v1/users/%d", kid.ID), `{"disabled":true}`, admin)
	if rec.Code != http.StatusOK {
		t.Fatalf("disable: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.do("GET", "/api/v1/auth/me", kidCookie); rec.Code != http.StatusUnauthorized {
		t.Errorf("after disable: HTTP %d, want 401", rec.Code)
	}
	if u, _ := s.auth.UserByID(context.Background(), kid.ID); u == nil || !u.Disabled {
		t.Error("user not marked disabled")
	}
	// Re-enabling lets them sign in again (with a fresh session).
	if rec := s.doBody("PUT", fmt.Sprintf("/api/v1/users/%d", kid.ID), `{"disabled":false}`, admin); rec.Code != http.StatusOK {
		t.Fatalf("enable: HTTP %d %s", rec.Code, rec.Body)
	}
	if _, err := s.auth.Authenticate(context.Background(), "kid@example.com", "password123"); err != nil {
		t.Errorf("re-enabled user can't sign in: %v", err)
	}
}

func TestCannotDisableSelfOrLastAdmin(t *testing.T) {
	s := newRouteServer(t, nil)
	owner, ownerCookie := s.user(t, "owner@example.com", auth.RoleAdmin)
	second, secondCookie := s.user(t, "second@example.com", auth.RoleAdmin)

	if rec := s.doBody("PUT", fmt.Sprintf("/api/v1/users/%d", owner.ID), `{"disabled":true}`, ownerCookie); rec.Code != http.StatusBadRequest {
		t.Errorf("disable self: HTTP %d, want 400", rec.Code)
	}
	// The second admin turns off the owner: fine, one enabled admin (second) remains.
	if rec := s.doBody("PUT", fmt.Sprintf("/api/v1/users/%d", owner.ID), `{"disabled":true}`, secondCookie); rec.Code != http.StatusOK {
		t.Fatalf("disable owner: HTTP %d %s", rec.Code, rec.Body)
	}
	// The owner's admin row still exists but can't sign in, so the second admin is the last
	// one who can: they can't demote themselves, even with a disable in the same request.
	for _, body := range []string{`{"role":"manager"}`, `{"role":"requester","disabled":true}`, `{"disabled":true}`} {
		rec := s.doBody("PUT", fmt.Sprintf("/api/v1/users/%d", second.ID), body, secondCookie)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s on the last enabled admin: HTTP %d, want 400", body, rec.Code)
		}
	}
	u, _ := s.auth.UserByID(context.Background(), second.ID)
	if u.Disabled || u.Role != auth.RoleAdmin {
		t.Errorf("last enabled admin changed: %+v", u)
	}
	// The guard counts admins who can sign in: had the owner still been enabled, the same
	// demote would have gone through.
	if rec := s.doBody("PUT", fmt.Sprintf("/api/v1/users/%d", owner.ID), `{"disabled":false}`, secondCookie); rec.Code != http.StatusOK {
		t.Fatalf("re-enable owner: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.doBody("PUT", fmt.Sprintf("/api/v1/users/%d", second.ID), `{"role":"manager"}`, secondCookie); rec.Code != http.StatusOK {
		t.Errorf("demote with another enabled admin: HTTP %d %s", rec.Code, rec.Body)
	}
}

// The Plex sign-in decision: blocked ids are refused before an account exists, disabled
// accounts after.
func TestPlexBlockedIDRejected(t *testing.T) {
	s := newRouteServer(t, nil)
	a := &api{deps: s.deps}
	ctx := context.Background()

	if ok, _ := a.plexSignInAllowed(ctx, "111", nil); !ok {
		t.Error("an unblocked id should be allowed")
	}
	if _, err := a.addPlexBlock(ctx, "111", "Grandad"); err != nil {
		t.Fatal(err)
	}
	if ok, msg := a.plexSignInAllowed(ctx, "111", nil); ok || !strings.Contains(msg, "isn't allowed") {
		t.Errorf("blocked id: ok=%v msg=%q", ok, msg)
	}
	if ok, msg := a.plexSignInAllowed(ctx, "222", &auth.User{Disabled: true}); ok || !strings.Contains(msg, "disabled") {
		t.Errorf("disabled account: ok=%v msg=%q", ok, msg)
	}
	if removed, err := a.removePlexBlock(ctx, "111"); err != nil || !removed {
		t.Fatalf("unblock: removed=%v err=%v", removed, err)
	}
	if ok, _ := a.plexSignInAllowed(ctx, "111", nil); !ok {
		t.Error("unblocking should let the id sign in again")
	}
}

// Deleting with ?block_plex=1 blocks their Plex account first; Unblock restores it.
func TestDeleteWithBlockPlex(t *testing.T) {
	snap := &snapshotRecorder{}
	s := newRouteServer(t, func(d *Deps) { d.Snapshot = snap.fn })
	a := &api{deps: s.deps}
	ctx := context.Background()
	_, admin := s.user(t, "owner@example.com", auth.RoleAdmin)
	local, _ := s.user(t, "local@example.com", auth.RoleRequester)
	px, err := s.auth.FindOrCreatePlexUser(ctx, "4242", "Grandad", auth.RoleRequester, auth.AutoApproval{})
	if err != nil {
		t.Fatal(err)
	}

	// Not linked to Plex: nothing to block, and nothing is deleted.
	if rec := s.do("DELETE", fmt.Sprintf("/api/v1/users/%d?block_plex=1", local.ID), admin); rec.Code != http.StatusBadRequest {
		t.Errorf("block_plex on a local account: HTTP %d, want 400", rec.Code)
	}
	if !s.userExists(t, local.ID) {
		t.Fatal("a refused delete removed the user")
	}
	if rec := s.do("GET", "/api/v1/users", admin); !strings.Contains(rec.Body.String(), `"plex_linked":true`) {
		t.Errorf("users list doesn't mark the Plex-linked user: %s", rec.Body)
	}

	if rec := s.do("DELETE", fmt.Sprintf("/api/v1/users/%d?block_plex=1", px.ID), admin); rec.Code != http.StatusNoContent {
		t.Fatalf("delete+block: HTTP %d %s", rec.Code, rec.Body)
	}
	if s.userExists(t, px.ID) {
		t.Error("user should be deleted")
	}
	rec := s.do("GET", "/api/v1/users/plex-blocks", admin)
	var out struct {
		Blocks []plexBlock `json:"blocks"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Blocks) != 1 || out.Blocks[0].PlexID != "4242" || out.Blocks[0].Name != "Grandad" {
		t.Fatalf("blocks = %+v (%s)", out.Blocks, rec.Body)
	}
	if ok, _ := a.plexSignInAllowed(ctx, "4242", nil); ok {
		t.Error("a deleted-and-blocked Plex account could sign in again")
	}

	if rec := s.do("DELETE", "/api/v1/users/plex-blocks/4242", admin); rec.Code != http.StatusOK {
		t.Fatalf("unblock: HTTP %d %s", rec.Code, rec.Body)
	}
	if ok, _ := a.plexSignInAllowed(ctx, "4242", nil); !ok {
		t.Error("unblock didn't restore Plex sign-in")
	}
	if rec := s.do("DELETE", "/api/v1/users/plex-blocks/4242", admin); rec.Code != http.StatusNotFound {
		t.Errorf("unblocking twice: HTTP %d, want 404", rec.Code)
	}
}

// If the delete itself fails after the block was added, the block is taken back off.
func TestDeleteWithBlockPlexSnapshotFailureBlocksNothing(t *testing.T) {
	snap := &snapshotRecorder{fail: fmt.Errorf("disk full")}
	s := newRouteServer(t, func(d *Deps) { d.Snapshot = snap.fn })
	_, admin := s.user(t, "owner@example.com", auth.RoleAdmin)
	px, _ := s.auth.FindOrCreatePlexUser(context.Background(), "99", "Uncle", auth.RoleRequester, auth.AutoApproval{})
	if rec := s.do("DELETE", fmt.Sprintf("/api/v1/users/%d?block_plex=1", px.ID), admin); rec.Code != http.StatusInternalServerError {
		t.Fatalf("HTTP %d, want 500", rec.Code)
	}
	if (&api{deps: s.deps}).plexBlocked(context.Background(), "99") {
		t.Error("a delete that didn't happen still blocked the Plex account")
	}
}

// Blocking a linked user's Plex account without deleting them.
func TestBlockUserPlexRoute(t *testing.T) {
	s := newRouteServer(t, nil)
	_, admin := s.user(t, "owner@example.com", auth.RoleAdmin)
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)
	px, _ := s.auth.FindOrCreatePlexUser(context.Background(), "77", "Cousin", auth.RoleRequester, auth.AutoApproval{})

	if rec := s.do("POST", fmt.Sprintf("/api/v1/users/%d/block-plex", px.ID), kid); rec.Code != http.StatusForbidden {
		t.Errorf("requester: HTTP %d, want 403", rec.Code)
	}
	if rec := s.do("POST", fmt.Sprintf("/api/v1/users/%d/block-plex", px.ID), admin); rec.Code != http.StatusOK {
		t.Fatalf("block: HTTP %d %s", rec.Code, rec.Body)
	}
	if !s.userExists(t, px.ID) {
		t.Error("blocking must not delete the account")
	}
	if !(&api{deps: s.deps}).plexBlocked(context.Background(), "77") {
		t.Error("blocked Plex id not on the list")
	}
	if rec := s.do("GET", "/api/v1/users", admin); !strings.Contains(rec.Body.String(), `"plex_blocked":true`) {
		t.Errorf("users list doesn't show the block: %s", rec.Body)
	}
}
