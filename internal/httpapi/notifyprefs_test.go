package httpapi

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/requests"
)

// Notification choices are the caller's own: a change by one person never shows up for
// another, unknown keys are ignored, and keys left out keep their value.
func TestNotifyPrefsEndpointScopedToCaller(t *testing.T) {
	s := newRouteServer(t, func(d *Deps) {
		d.Requests = requests.NewService(d.Store.DB(), nil, nil, nil, nil, nil, nil, "", d.Log)
	})
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)
	_, aunt := s.user(t, "aunt@example.com", auth.RoleRequester)

	read := func(c *http.Cookie) map[string]bool {
		t.Helper()
		rec := s.do("GET", "/api/v1/me/notify-prefs", c)
		if rec.Code != http.StatusOK {
			t.Fatalf("GET: HTTP %d %s", rec.Code, rec.Body)
		}
		var p map[string]bool
		if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if p := read(kid); len(p) != 4 || !p["approved"] || !p["declined"] || !p["ready"] || !p["new_request"] {
		t.Fatalf("defaults = %v, want every key on", p)
	}
	if rec := s.doJSON("PUT", "/api/v1/me/notify-prefs", kid, `{"approved":false,"made_up":false}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.doJSON("PUT", "/api/v1/me/notify-prefs", kid, `{"ready":false}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT: HTTP %d %s", rec.Code, rec.Body)
	}
	if p := read(kid); p["approved"] || p["ready"] || !p["declined"] || len(p) != 4 {
		t.Errorf("kid's prefs = %v, want approved and ready off, nothing unknown", p)
	}
	if p := read(aunt); !p["approved"] || !p["ready"] {
		t.Errorf("aunt's prefs = %v: another user's change leaked", p)
	}
	if rec := s.doJSON("PUT", "/api/v1/me/notify-prefs", kid, `{"approved":"no"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a non-boolean: HTTP %d, want 400", rec.Code)
	}
	if rec := s.do("GET", "/api/v1/me/notify-prefs", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("signed out: HTTP %d, want 401", rec.Code)
	}
}
