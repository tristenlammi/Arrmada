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

// ownerSignIn signs in with Plex as the server's owner (plexLoginServer's owner-token, which
// owns M1) and returns the answer.
func ownerSignIn(t *testing.T, s *routeServer, tv *fakePlexTV) (int, string) {
	t.Helper()
	id, _, pc := startPin(t, s, "/api/v1/auth/plex/pin", nil, nil)
	tv.approve(id, "owner-token")
	rec := s.doCookies("GET", fmt.Sprintf("/api/v1/auth/plex/pin/%d", id), "", pc)
	return rec.Code, rec.Body.String()
}

// With staff Plex sign-in on and exactly one admin, the owner's Plex account is linked to
// that admin and signs straight into it.
func TestPlexLoginOwnerMapsToAdminWhenAllowed(t *testing.T) {
	s, tv := plexLoginServer(t)
	admin, _ := s.user(t, "owner@example.com", auth.RoleAdmin)
	_ = s.deps.Settings.SetBool(context.Background(), keyPlexSignInStaff, true)
	code, body := ownerSignIn(t, s, tv)
	if code != http.StatusOK || !strings.Contains(body, `"role":"admin"`) {
		t.Fatalf("HTTP %d %s, want the admin session", code, body)
	}
	if got := s.auth.PlexIDForUser(context.Background(), admin.ID); got != "1" {
		t.Errorf("admin plex id = %q, want the owner's (1)", got)
	}
	// And again, now through the link.
	if code, body := ownerSignIn(t, s, tv); code != http.StatusOK || !strings.Contains(body, `"role":"admin"`) {
		t.Errorf("second sign-in: HTTP %d %s", code, body)
	}
}

// Two admins: which one is the owner can't be told, so 409 with instructions — and no
// requester account is made for the owner.
func TestPlexLoginOwnerAmbiguousAdmins409(t *testing.T) {
	s, tv := plexLoginServer(t)
	s.user(t, "owner@example.com", auth.RoleAdmin)
	s.user(t, "partner@example.com", auth.RoleAdmin)
	_ = s.deps.Settings.SetBool(context.Background(), keyPlexSignInStaff, true)
	before, _ := s.auth.UserCount(context.Background())
	if code, body := ownerSignIn(t, s, tv); code != http.StatusConflict || !strings.Contains(body, "owns the server") {
		t.Fatalf("HTTP %d %s, want 409", code, body)
	}
	if after, _ := s.auth.UserCount(context.Background()); after != before {
		t.Errorf("users %d → %d: an account was made for the owner", before, after)
	}
}

// With the policy off (the default) the owner is refused the same way, and a linked staff
// account stays shut to Plex sign-in until the policy is on.
func TestPlexLoginOwnerSettingOff409(t *testing.T) {
	s, tv := plexLoginServer(t)
	admin, _ := s.user(t, "owner@example.com", auth.RoleAdmin)
	before, _ := s.auth.UserCount(context.Background())
	if code, _ := ownerSignIn(t, s, tv); code != http.StatusConflict {
		t.Fatalf("HTTP %d, want 409", code)
	}
	if after, _ := s.auth.UserCount(context.Background()); after != before || s.auth.PlexIDForUser(context.Background(), admin.ID) != "" {
		t.Error("the owner's sign-in changed the accounts")
	}
	// Linked by hand: still refused while the policy is off, let in once it's on.
	_ = s.auth.LinkPlex(context.Background(), admin.ID, "1", "owner")
	if code, body := ownerSignIn(t, s, tv); code != http.StatusForbidden || !strings.Contains(body, "staff account") {
		t.Errorf("linked admin, policy off: HTTP %d %s", code, body)
	}
	_ = s.deps.Settings.SetBool(context.Background(), keyPlexSignInStaff, true)
	if code, _ := ownerSignIn(t, s, tv); code != http.StatusOK {
		t.Errorf("linked admin, policy on: HTTP %d", code)
	}
}

// Only an admin may change the staff sign-in policy.
func TestPlexSignInStaffIsAdminOnly(t *testing.T) {
	s := newRouteServer(t, nil)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	_, admin := s.user(t, "owner@example.com", auth.RoleAdmin)
	if rec := s.doBody("PUT", "/api/v1/settings", `{"plex_signin_staff":true}`, mgr); rec.Code != http.StatusForbidden {
		t.Errorf("manager: HTTP %d, want 403", rec.Code)
	}
	if rec := s.doBody("PUT", "/api/v1/settings", `{"plex_signin_staff":true}`, admin); rec.Code != http.StatusOK {
		t.Fatalf("admin: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.do("GET", "/api/v1/settings", admin); !strings.Contains(rec.Body.String(), `"plex_signin_staff":true`) {
		t.Errorf("settings = %s", rec.Body)
	}
}

// The merge endpoint previews in counts, copies the database first and then merges; a
// failed copy merges nothing.
func TestPlexMergeEndpoint(t *testing.T) {
	snap := &snapshotRecorder{}
	s := newRouteServer(t, func(d *Deps) { d.Snapshot = snap.fn })
	ctx := context.Background()
	admin, adminCookie := s.user(t, "owner@example.com", auth.RoleAdmin)
	dup, _ := s.auth.FindOrCreatePlexUser(ctx, "4242", "ownerplex", auth.RoleRequester, auth.AutoApproval{})
	s.giveListeningData(t, dup.ID)
	if _, err := s.st.DB().Exec(`INSERT INTO requests (media_type, tmdb_id, title, requested_by, requested_by_name) VALUES ('movie', 1, 'A', ?, 'ownerplex')`, dup.ID); err != nil {
		t.Fatal(err)
	}

	rec := s.do("GET", fmt.Sprintf("/api/v1/users/%d/plex/merge?from=%d", admin.ID, dup.ID), adminCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("preview: HTTP %d %s", rec.Code, rec.Body)
	}
	var p auth.MergePreview
	_ = json.Unmarshal(rec.Body.Bytes(), &p)
	if p.Requests != 1 || !p.AudiobookProgress {
		t.Errorf("preview = %+v", p)
	}
	if strings.Contains(rec.Body.String(), "b1") {
		t.Errorf("the preview names a book: %s", rec.Body)
	}

	// The copy fails: nothing is merged.
	snap.fail = fmt.Errorf("disk full")
	body := fmt.Sprintf(`{"from_user_id":%d}`, dup.ID)
	if rec := s.doBody("POST", fmt.Sprintf("/api/v1/users/%d/plex/merge", admin.ID), body, adminCookie); rec.Code != http.StatusInternalServerError {
		t.Errorf("failed copy: HTTP %d", rec.Code)
	}
	if !s.userExists(t, dup.ID) {
		t.Fatal("merged without a safety copy")
	}
	snap.fail = nil
	rec = s.doBody("POST", fmt.Sprintf("/api/v1/users/%d/plex/merge", admin.ID), body, adminCookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("merge: HTTP %d %s", rec.Code, rec.Body)
	}
	if snap.calls[len(snap.calls)-1] != "pre-merge-user" || s.userExists(t, dup.ID) || s.auth.PlexIDForUser(ctx, admin.ID) != "4242" {
		t.Errorf("after merge: snapshots %v, dup exists %v, admin plex %q", snap.calls, s.userExists(t, dup.ID), s.auth.PlexIDForUser(ctx, admin.ID))
	}
	// Merging a staff account away is refused.
	mgr, _ := s.user(t, "mgr@example.com", auth.RoleManager)
	_ = s.auth.LinkPlex(ctx, mgr.ID, "555", "m")
	other, _ := s.user(t, "other@example.com", auth.RoleRequester)
	if rec := s.doBody("POST", fmt.Sprintf("/api/v1/users/%d/plex/merge", other.ID), fmt.Sprintf(`{"from_user_id":%d}`, mgr.ID), adminCookie); rec.Code != http.StatusBadRequest {
		t.Errorf("merging a manager away: HTTP %d, want 400", rec.Code)
	}
}
