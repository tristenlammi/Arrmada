package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/plex"
)

// Linking: pending until Plex approves, then the Plex id and name are stored on the
// signed-in account. Only the account (and browser) that started the PIN can finish it.
func TestMePlexLinkFlow(t *testing.T) {
	tv := newFakePlexTV(t)
	s := newRouteServer(t, nil)
	admin, adminCookie := s.user(t, "owner@example.com", auth.RoleAdmin)
	_, otherCookie := s.user(t, "other@example.com", auth.RoleRequester)
	tv.account("owner-plex-token", plex.Account{ID: 4242, Username: "ownerplex"})

	id, fwd, pinCookie := startPin(t, s, "/api/v1/me/plex/link?mode=redirect", adminCookie, nil)
	if want := fmt.Sprintf("http://arrmada.local/discover?plexlink=%d", id); fwd != want {
		t.Errorf("forwardUrl = %q, want %q", fwd, want)
	}
	poll := fmt.Sprintf("/api/v1/me/plex/link/%d", id)
	rec := s.doCookies("GET", poll, "", adminCookie, pinCookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"pending":true`) {
		t.Fatalf("pending: HTTP %d %s", rec.Code, rec.Body)
	}
	tv.approve(id, "owner-plex-token")
	// Someone else's session, even carrying the PIN cookie, can't take the link.
	if rec := s.doCookies("GET", poll, "", otherCookie, pinCookie); rec.Code != http.StatusForbidden {
		t.Errorf("other account: HTTP %d, want 403", rec.Code)
	}
	rec = s.doCookies("GET", poll, "", adminCookie, pinCookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"plex_username":"ownerplex"`) {
		t.Fatalf("linked: HTTP %d %s", rec.Code, rec.Body)
	}
	if got := s.auth.PlexIDForUser(context.Background(), admin.ID); got != "4242" {
		t.Errorf("plex id = %q, want 4242", got)
	}
	rec = s.doCookies("GET", "/api/v1/me/plex", "", adminCookie)
	if !strings.Contains(rec.Body.String(), `"linked":true`) || !strings.Contains(rec.Body.String(), `"can_unlink":true`) {
		t.Errorf("me/plex = %s", rec.Body)
	}
	// The users list shows the linked name.
	if rec := s.do("GET", "/api/v1/users", adminCookie); !strings.Contains(rec.Body.String(), `"plex_username":"ownerplex"`) {
		t.Errorf("users list lacks the Plex name: %s", rec.Body)
	}
	// Unlink clears it.
	if rec := s.doCookies("DELETE", "/api/v1/me/plex/link", "", adminCookie); rec.Code != http.StatusOK {
		t.Fatalf("unlink: HTTP %d %s", rec.Code, rec.Body)
	}
	if got := s.auth.PlexIDForUser(context.Background(), admin.ID); got != "" {
		t.Errorf("after unlink plex id = %q", got)
	}
}

// A Plex account already linked elsewhere: everyone is told who has it; only an admin gets
// the id to merge, and only for a requester's link.
func TestMePlexLinkConflict(t *testing.T) {
	tv := newFakePlexTV(t)
	s := newRouteServer(t, nil)
	ctx := context.Background()
	_, adminCookie := s.user(t, "owner@example.com", auth.RoleAdmin)
	_, mumCookie := s.user(t, "mum@example.com", auth.RoleRequester)
	dup, _ := s.auth.FindOrCreatePlexUser(ctx, "4242", "ownerplex", auth.RoleRequester, auth.AutoApproval{})
	tv.account("tok", plex.Account{ID: 4242, Username: "ownerplex"})

	link := func(c *http.Cookie) map[string]any {
		id, _, pc := startPin(t, s, "/api/v1/me/plex/link", c, nil)
		tv.approve(id, "tok")
		rec := s.doCookies("GET", fmt.Sprintf("/api/v1/me/plex/link/%d", id), "", c, pc)
		if rec.Code != http.StatusConflict {
			t.Fatalf("HTTP %d %s, want 409", rec.Code, rec.Body)
		}
		var out map[string]any
		_ = json.Unmarshal(rec.Body.Bytes(), &out)
		return out
	}
	if out := link(adminCookie); out["already_linked_to"] != "ownerplex" || out["user_id"] != float64(dup.ID) || out["mergeable"] != true {
		t.Errorf("admin conflict = %v", out)
	}
	if out := link(mumCookie); out["already_linked_to"] != "ownerplex" || out["user_id"] != nil || out["mergeable"] != nil {
		t.Errorf("requester conflict = %v (must not carry ids)", out)
	}
}

// Linking a staff account must not open a Plex door into it.
func TestPlexLoginRefusesLinkedStaff(t *testing.T) {
	s, tv := plexLoginServer(t)
	admin, _ := s.user(t, "owner@example.com", auth.RoleAdmin)
	if err := s.auth.LinkPlex(context.Background(), admin.ID, "4242", "ownerplex"); err != nil {
		t.Fatal(err)
	}
	tv.account("owner-plex-token", plex.Account{ID: 4242, Username: "ownerplex"}, fakeResource{Name: "Home", ID: "M1"})
	id, _, pc := startPin(t, s, "/api/v1/auth/plex/pin", nil, nil)
	tv.approve(id, "owner-plex-token")
	rec := s.doCookies("GET", fmt.Sprintf("/api/v1/auth/plex/pin/%d", id), "", pc)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "staff account") {
		t.Fatalf("HTTP %d %s, want 403 naming the staff account", rec.Code, rec.Body)
	}
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName && c.Value != "" {
			t.Error("a session cookie was set")
		}
	}
}

// Plex is a Plex-made account's only way in: it can't unlink itself into a lock-out; an
// admin can remove the link from Settings → Users.
func TestPlexUnlinkGuards(t *testing.T) {
	s := newRouteServer(t, nil)
	ctx := context.Background()
	_, adminCookie := s.user(t, "owner@example.com", auth.RoleAdmin)
	kid, _ := s.auth.FindOrCreatePlexUser(ctx, "777", "kid", auth.RoleRequester, auth.AutoApproval{})
	tok, _, _ := s.auth.CreateSession(ctx, kid.ID)
	kidCookie := &http.Cookie{Name: sessionCookieName, Value: tok}

	if rec := s.doCookies("DELETE", "/api/v1/me/plex/link", "", kidCookie); rec.Code != http.StatusConflict {
		t.Errorf("self-unlink of a Plex-only account: HTTP %d, want 409", rec.Code)
	}
	if rec := s.do("GET", "/api/v1/users", adminCookie); !strings.Contains(rec.Body.String(), `"plex_only":true`) {
		t.Errorf("users list doesn't flag the Plex-only account: %s", rec.Body)
	}
	if rec := s.doBody("PUT", fmt.Sprintf("/api/v1/users/%d", kid.ID), `{"plex_unlink":true}`, adminCookie); rec.Code != http.StatusOK {
		t.Fatalf("admin unlink: HTTP %d %s", rec.Code, rec.Body)
	}
	if got := s.auth.PlexIDForUser(ctx, kid.ID); got != "" {
		t.Errorf("after admin unlink plex id = %q", got)
	}
}
