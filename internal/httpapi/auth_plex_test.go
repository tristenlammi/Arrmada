package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/plex"
)

// The address plex.tv sends a redirect-mode sign-in back to is built from the request:
// scheme from TLS or a proxy's X-Forwarded-Proto, host from Host (or a local proxy's
// X-Forwarded-Host), and nothing that could make it point somewhere else.
func TestPlexForwardURL(t *testing.T) {
	cases := []struct {
		name   string
		host   string
		peer   string
		hdr    map[string]string
		want   string
		wantOK bool
	}{
		{"plain http", "arrmada.local:8080", "192.168.1.20:5000", nil, "http://arrmada.local:8080/?plexpin=7", true},
		{"https via proxy", "arrmada.local", "172.17.0.1:5000", map[string]string{"X-Forwarded-Proto": "https"}, "https://arrmada.local/?plexpin=7", true},
		{"forwarded host from a local proxy", "arrmada:8080", "172.17.0.1:5000", map[string]string{"X-Forwarded-Host": "requests.example.com, other", "X-Forwarded-Proto": "https"}, "https://requests.example.com/?plexpin=7", true},
		{"forwarded host from the internet is ignored", "arrmada.local", "203.0.113.9:5000", map[string]string{"X-Forwarded-Host": "evil.example"}, "http://arrmada.local/?plexpin=7", true},
		{"ipv6 literal", "[fd00::5]:8080", "192.168.1.20:5000", nil, "http://[fd00::5]:8080/?plexpin=7", true},
		{"slash in host", "evil.example/x", "192.168.1.20:5000", nil, "", false},
		{"userinfo in host", "me@evil.example", "192.168.1.20:5000", nil, "", false},
		{"forwarded host with a path", "arrmada.local", "172.17.0.1:5000", map[string]string{"X-Forwarded-Host": "evil.example/a"}, "", false},
		{"space in host", "arr mada", "192.168.1.20:5000", nil, "", false},
	}
	for _, tc := range cases {
		r := httptest.NewRequest("POST", "http://placeholder/api/v1/auth/plex/pin", nil)
		r.Host = tc.host
		r.RemoteAddr = tc.peer
		for k, v := range tc.hdr {
			r.Header.Set(k, v)
		}
		got, ok := plexForwardURL(r, "/?plexpin=7")
		if ok != tc.wantOK || got != tc.want {
			t.Errorf("%s: got %q ok=%v, want %q ok=%v", tc.name, got, ok, tc.want, tc.wantOK)
		}
	}
}

func plexLoginServer(t *testing.T) (*routeServer, *fakePlexTV) {
	t.Helper()
	tv := newFakePlexTV(t)
	s := newRouteServer(t, nil)
	ctx := context.Background()
	_ = s.deps.Settings.SetBool(ctx, "plex_login_enabled", true)
	_ = s.deps.Settings.Set(ctx, "insights_plex_token", "owner-token")
	tv.account("owner-token", plex.Account{ID: 1, Username: "owner"}, fakeResource{Name: "Home", ID: "M1", Owned: true})
	return s, tv
}

// Redirect mode hands plex.tv a forwardUrl built from the request only; anything the
// client adds to the query is ignored. Popup mode sends none.
func TestPlexLoginStartBuildsForwardURLFromRequest(t *testing.T) {
	s, _ := plexLoginServer(t)
	id, fwd, _ := startPin(t, s, "/api/v1/auth/plex/pin?mode=redirect&forwardUrl=https://evil.example/&forward=https://evil.example/", nil, map[string]string{"X-Forwarded-Proto": "https"})
	if want := fmt.Sprintf("https://arrmada.local/?plexpin=%d", id); fwd != want {
		t.Errorf("forwardUrl = %q, want %q", fwd, want)
	}
	if _, fwd, _ := startPin(t, s, "/api/v1/auth/plex/pin", nil, nil); fwd != "" {
		t.Errorf("popup mode forwardUrl = %q, want none", fwd)
	}
}

// A PIN can only be collected by the browser that started it: the id alone (which
// travels in URLs and is sequential at plex.tv) gets nothing.
func TestPlexLoginPollNeedsStartingBrowser(t *testing.T) {
	s, tv := plexLoginServer(t)
	tv.account("kid-token", plex.Account{ID: 555, Username: "kid"}, fakeResource{Name: "Home", ID: "M1"})
	id, _, cookie := startPin(t, s, "/api/v1/auth/plex/pin", nil, nil)
	poll := fmt.Sprintf("/api/v1/auth/plex/pin/%d", id)

	if rec := s.doCookies("GET", poll, ""); rec.Code != http.StatusForbidden {
		t.Errorf("no cookie: HTTP %d, want 403", rec.Code)
	}
	forged := &http.Cookie{Name: cookie.Name, Value: "guess"}
	if rec := s.doCookies("GET", poll, "", forged); rec.Code != http.StatusForbidden {
		t.Errorf("wrong cookie: HTTP %d, want 403", rec.Code)
	}
	rec := s.doCookies("GET", poll, "", cookie)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"pending":true`) {
		t.Fatalf("pending: HTTP %d %s", rec.Code, rec.Body)
	}
	tv.approve(id, "kid-token")
	if rec := s.doCookies("GET", poll, "", forged); rec.Code != http.StatusForbidden {
		t.Errorf("approved, wrong cookie: HTTP %d, want 403", rec.Code)
	}
	rec = s.doCookies("GET", poll, "", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("approved: HTTP %d %s", rec.Code, rec.Body)
	}
	var out struct {
		User auth.User `json:"user"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out.User.Role != auth.RoleRequester || out.User.Username != "kid" {
		t.Errorf("signed in as %+v, want the requester kid", out.User)
	}
	// Spent: polling it again gets nothing.
	if rec := s.doCookies("GET", poll, "", cookie); rec.Code != http.StatusForbidden {
		t.Errorf("reused PIN: HTTP %d, want 403", rec.Code)
	}
}

// Arrmada keeps a handle to popups it opens (the Plex sign-in window) while still
// refusing one to cross-origin openers.
func TestSecurityHeadersCOOP(t *testing.T) {
	s := newRouteServer(t, nil)
	rec := s.do("GET", "/api/health", nil)
	if got := rec.Header().Get("Cross-Origin-Opener-Policy"); got != "same-origin-allow-popups" {
		t.Errorf("COOP = %q, want same-origin-allow-popups", got)
	}
}
