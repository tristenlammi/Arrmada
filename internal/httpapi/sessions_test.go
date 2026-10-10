package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
)

type listedSession struct {
	ID      string `json:"id"`
	Device  string `json:"device"`
	Network string `json:"network"`
	Current bool   `json:"current"`
}

func listMySessions(t *testing.T, s *routeServer, c *http.Cookie) []listedSession {
	t.Helper()
	rec := s.do("GET", "/api/v1/me/sessions", c)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /me/sessions: HTTP %d %s", rec.Code, rec.Body)
	}
	var out struct {
		Sessions []listedSession `json:"sessions"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out.Sessions
}

// Signing in records the browser and a coarse network; the list shows only your own
// sessions with this one marked; you can end one of yours but never someone else's.
func TestMySessionsListAndRevoke(t *testing.T) {
	s := newRouteServer(t, nil)
	ctx := context.Background()
	if _, err := s.auth.CreateUser(ctx, "kid@example.com", "password123", auth.RoleRequester, false); err != nil {
		t.Fatal(err)
	}
	// A real sign-in, from a phone outside the house behind the local proxy.
	r := httptest.NewRequest("POST", "http://arrmada.local/api/v1/auth/login", strings.NewReader(`{"username":"kid@example.com","password":"password123"}`))
	r.RemoteAddr = "127.0.0.1:5000"
	r.Header.Set("X-Forwarded-For", "203.0.113.77")
	r.Header.Set("User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1")
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("login: HTTP %d %s", rec.Code, rec.Body)
	}
	var phone *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			phone = c
		}
	}
	u, _ := s.auth.UserByUsername(ctx, "kid@example.com")
	laptopTok, _, _ := s.auth.CreateSession(ctx, u.ID)
	laptop := &http.Cookie{Name: sessionCookieName, Value: laptopTok}
	_, aunt := s.user(t, "aunt@example.com", auth.RoleRequester)

	list := listMySessions(t, s, phone)
	if len(list) != 2 {
		t.Fatalf("listed %d sessions, want 2: %+v", len(list), list)
	}
	var phoneRow, laptopRow listedSession
	for _, l := range list {
		if l.Current {
			phoneRow = l
		} else {
			laptopRow = l
		}
	}
	if phoneRow.Device != "Safari on iPhone" || phoneRow.Network != "203.0.113.x" {
		t.Errorf("this device = %+v", phoneRow)
	}
	if strings.Contains(s.do("GET", "/api/v1/me/sessions", phone).Body.String(), "203.0.113.77") {
		t.Error("the full address is in the list")
	}
	if got := listMySessions(t, s, aunt); len(got) != 1 || !got[0].Current {
		t.Errorf("aunt sees %+v, want only her own session", got)
	}

	// The aunt can't end the kid's laptop session by its id.
	if rec := s.doCookies("DELETE", "/api/v1/me/sessions/"+laptopRow.ID, "", aunt); rec.Code != http.StatusNotFound {
		t.Errorf("another user's session: HTTP %d, want 404", rec.Code)
	}
	if rec := s.do("GET", "/api/v1/auth/me", laptop); rec.Code != http.StatusOK {
		t.Fatalf("laptop signed out by someone else: HTTP %d", rec.Code)
	}
	if rec := s.doCookies("DELETE", "/api/v1/me/sessions/"+laptopRow.ID, "", phone); rec.Code != http.StatusNoContent {
		t.Fatalf("own session: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.do("GET", "/api/v1/auth/me", laptop); rec.Code != http.StatusUnauthorized {
		t.Errorf("revoked laptop still in: HTTP %d", rec.Code)
	}
	if rec := s.do("GET", "/api/v1/auth/me", phone); rec.Code != http.StatusOK {
		t.Errorf("this device signed out: HTTP %d", rec.Code)
	}
}

// "Sign out everywhere" is admin-only, ends every session of that user, answers with a
// count and no session details, and the log names who did it to whom — no tokens or
// addresses.
func TestAdminSignOutEverywhere(t *testing.T) {
	var logs bytes.Buffer
	s := newRouteServer(t, func(d *Deps) { d.Log = slog.New(slog.NewTextHandler(&logs, nil)) })
	ctx := context.Background()
	admin, adminC := s.user(t, "owner@example.com", auth.RoleAdmin)
	_, mgrC := s.user(t, "mgr@example.com", auth.RoleManager)
	kid, kidC := s.user(t, "kid@example.com", auth.RoleRequester)
	other, _, _ := s.auth.CreateSessionFrom(ctx, kid.ID, auth.SessionClient{UserAgent: "UA-SECRET", IP: "198.51.100.9"})
	path := "/api/v1/users/" + strconv.FormatInt(kid.ID, 10) + "/sessions/revoke"

	for _, c := range []*http.Cookie{kidC, mgrC} {
		if rec := s.doJSON("POST", path, c, ``); rec.Code != http.StatusForbidden {
			t.Errorf("non-admin: HTTP %d, want 403", rec.Code)
		}
	}
	rec := s.doJSON("POST", path, adminC, ``)
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"signed_out":2}` {
		t.Fatalf("admin: HTTP %d %s", rec.Code, rec.Body)
	}
	for _, c := range []*http.Cookie{kidC, {Name: sessionCookieName, Value: other}} {
		if rec := s.do("GET", "/api/v1/auth/me", c); rec.Code != http.StatusUnauthorized {
			t.Errorf("kid still signed in: HTTP %d", rec.Code)
		}
	}
	if rec := s.do("GET", "/api/v1/auth/me", adminC); rec.Code != http.StatusOK {
		t.Errorf("the admin was signed out: HTTP %d", rec.Code)
	}
	line := logs.String()
	if !strings.Contains(line, "signed out everywhere by an admin") || !strings.Contains(line, "by_user_id="+strconv.FormatInt(admin.ID, 10)) {
		t.Errorf("no audit line naming the admin:\n%s", line)
	}
	for _, secret := range []string{other, kidC.Value, "198.51.100", "UA-SECRET"} {
		if strings.Contains(line, secret) {
			t.Errorf("the log holds %q", secret)
		}
	}
	if rec := s.doJSON("POST", "/api/v1/users/9999/sessions/revoke", adminC, ``); rec.Code != http.StatusNotFound {
		t.Errorf("unknown user: HTTP %d, want 404", rec.Code)
	}
}
