package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/metadata"
)

// stubDiscovery is a DiscoveryProvider whose only real answer is Available(); the feeds
// themselves are never reached in these tests.
type stubDiscovery struct {
	metadata.DiscoveryProvider
	ok bool
}

func (s stubDiscovery) Available() bool { return s.ok }

func TestMetadataReady(t *testing.T) {
	cases := []struct {
		name string
		d    metadata.DiscoveryProvider
		want bool
	}{
		{"no provider", nil, false},
		{"no key", stubDiscovery{ok: false}, false},
		{"key set", stubDiscovery{ok: true}, true},
	}
	for _, c := range cases {
		a := &api{deps: Deps{Discovery: c.d}}
		if got := a.metadataReady(); got != c.want {
			t.Errorf("%s: metadataReady() = %v, want %v", c.name, got, c.want)
		}
	}
}

// Without a key, each role gets a message it can act on: the admin is pointed at the key,
// a manager is told to ask an admin, and a requester never hears about Settings at all.
func TestDiscoveryReadyMessageByRole(t *testing.T) {
	a := &api{}
	call := func(u *auth.User) (int, string) {
		r := httptest.NewRequest("GET", "/api/v1/discover/trending", nil)
		if u != nil {
			r = withUser(r, u)
		}
		rec := httptest.NewRecorder()
		if a.discoveryReady(rec, r) {
			t.Fatal("discoveryReady = true with no provider")
		}
		var body struct{ Message string }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body.Message
	}

	for _, c := range []struct {
		role auth.Role
		want string
	}{
		{auth.RoleAdmin, discoveryOffAdmin},
		{auth.RoleManager, discoveryOffManager},
		{auth.RoleRequester, discoveryOffOthers},
		{auth.RoleReadonly, discoveryOffOthers},
	} {
		code, msg := call(&auth.User{ID: 1, Username: "u", Role: c.role})
		if code != http.StatusBadRequest || msg != c.want {
			t.Errorf("%s: %d %q, want 400 %q", c.role, code, msg, c.want)
		}
	}
	if _, msg := call(nil); msg != discoveryOffOthers {
		t.Errorf("no user: %q, want the plain message", msg)
	}
	for _, m := range []string{discoveryOffManager, discoveryOffOthers} {
		if strings.Contains(m, "add a TMDB key in") {
			t.Errorf("non-admin message tells them to add the key themselves: %q", m)
		}
	}
	if strings.Contains(discoveryOffOthers, "Settings") || strings.Contains(discoveryOffOthers, "TMDB") {
		t.Errorf("requester message mentions configuration: %q", discoveryOffOthers)
	}

	a.deps.Discovery = stubDiscovery{ok: true}
	rec := httptest.NewRecorder()
	if !a.discoveryReady(rec, httptest.NewRequest("GET", "/", nil)) {
		t.Error("discoveryReady = false with a key set")
	}
}

// /status tells a signed-in caller whether metadata works, and an anonymous one nothing.
func TestStatusMetadataReadyOnlyWhenSignedIn(t *testing.T) {
	h, _, session := musicGateServer(t)
	read := func(session string) map[string]any {
		var body map[string]any
		rec := musicGateCall(h, "GET", "/api/v1/status", session)
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		return body
	}
	if _, ok := read("")["metadata_ready"]; ok {
		t.Error("anonymous /status carries metadata_ready")
	}
	if v, ok := read(session)["metadata_ready"]; !ok || v != false {
		t.Errorf("signed-in /status metadata_ready = %v (present %v), want false", v, ok)
	}
}
