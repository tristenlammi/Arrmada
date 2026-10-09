package httpapi

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
)

// sessionCookieOn returns the session cookie a response sets, if any.
func sessionCookieOn(rec interface{ Result() *http.Response }) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookieName {
			return c
		}
	}
	return nil
}

// A session past half its life is extended on use and the browser is sent the new expiry;
// one before half-life is left alone (no write, no cookie).
func TestSessionSlidesPastHalfLife(t *testing.T) {
	s := newRouteServer(t, nil)
	_, cookie := s.user(t, "kid@example.com", auth.RoleRequester)

	// Fresh session: nothing to do.
	rec := s.do("GET", "/api/v1/auth/me", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d", rec.Code)
	}
	if c := sessionCookieOn(rec); c != nil {
		t.Errorf("a fresh session re-set its cookie (expires %v)", c.Expires)
	}

	// Ten days left of thirty: past half-life.
	soon := time.Now().UTC().Add(10 * 24 * time.Hour).Format("2006-01-02 15:04:05")
	if _, err := s.st.DB().Exec(`UPDATE sessions SET expires_at = ?`, soon); err != nil {
		t.Fatal(err)
	}
	// A WebSocket upgrade can't carry the new cookie, so it doesn't slide the session.
	up := httptest.NewRequest("GET", "http://arrmada.local/api/v1/auth/me", nil)
	up.RemoteAddr = "192.168.1.20:5000"
	up.Header.Set("Upgrade", "websocket")
	up.AddCookie(cookie)
	upRec := httptest.NewRecorder()
	s.h.ServeHTTP(upRec, up)
	var afterUpgrade string
	_ = s.st.DB().QueryRow(`SELECT strftime('%Y-%m-%d %H:%M:%S', expires_at) FROM sessions`).Scan(&afterUpgrade)
	if sessionCookieOn(upRec) != nil || afterUpgrade != soon {
		t.Errorf("an upgrade request slid the session (stored %q, want %q)", afterUpgrade, soon)
	}

	rec = s.do("GET", "/api/v1/auth/me", cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d", rec.Code)
	}
	c := sessionCookieOn(rec)
	if c == nil {
		t.Fatal("a session past half-life wasn't extended")
	}
	if c.Value != cookie.Value || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode {
		t.Errorf("re-set cookie changed shape: %+v", c)
	}
	if left := time.Until(c.Expires); left < 29*24*time.Hour {
		t.Errorf("new cookie expires in %v, want about 30 days", left)
	}
	var stored string
	_ = s.st.DB().QueryRow(`SELECT strftime('%Y-%m-%d %H:%M:%S', expires_at) FROM sessions`).Scan(&stored)
	if stored <= soon {
		t.Errorf("stored expiry %q didn't move past %q", stored, soon)
	}

	// An expired session is still refused.
	past := time.Now().UTC().Add(-time.Minute).Format("2006-01-02 15:04:05")
	_, _ = s.st.DB().Exec(`UPDATE sessions SET expires_at = ?`, past)
	if rec := s.do("GET", "/api/v1/auth/me", cookie); rec.Code != http.StatusUnauthorized {
		t.Errorf("expired session: HTTP %d, want 401", rec.Code)
	}
}
