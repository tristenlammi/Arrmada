package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/apikeys"
	"github.com/tristenlammi/arrmada/internal/auth"
)

// doBody is do with a JSON body.
func (s *routeServer) doBody(method, path, body string, c *http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest(method, "http://arrmada.local"+path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.RemoteAddr = "192.168.1.20:5000"
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	s.h.ServeHTTP(rec, r)
	return rec
}

func newKeyServer(t *testing.T) *routeServer {
	t.Helper()
	return newRouteServer(t, func(d *Deps) { d.APIKeys = apikeys.NewStore(d.Settings) })
}

func keyStatus(t *testing.T, rec *httptest.ResponseRecorder, id string) apikeys.KeyStatus {
	t.Helper()
	var out struct {
		Keys []apikeys.KeyStatus `json:"keys"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body)
	}
	for _, k := range out.Keys {
		if k.ID == id {
			return k
		}
	}
	t.Fatalf("%s missing from %s", id, rec.Body)
	return apikeys.KeyStatus{}
}

// An empty Save used to wipe a working key. Now it's a 400 and the key stays.
func TestSetAPIKeyEmptyRejected(t *testing.T) {
	s := newKeyServer(t)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	if rec := s.doBody("PUT", "/api/v1/apikeys/tmdb", `{"value":"abcd1234"}`, mgr); rec.Code != http.StatusOK {
		t.Fatalf("save: HTTP %d %s", rec.Code, rec.Body)
	}
	for _, body := range []string{`{"value":""}`, `{"value":"   "}`, `{}`} {
		rec := s.doBody("PUT", "/api/v1/apikeys/tmdb", body, mgr)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "use Clear") {
			t.Errorf("%s: HTTP %d %s, want 400 pointing at Clear", body, rec.Code, rec.Body)
		}
	}
	if got := s.deps.APIKeys.Value(t.Context(), "tmdb"); got != "abcd1234" {
		t.Errorf("key after refused blank saves = %q, want it kept", got)
	}
	if rec := s.doBody("PUT", "/api/v1/apikeys/nope", `{"value":"x"}`, mgr); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: HTTP %d, want 404", rec.Code)
	}
}

// DELETE clears the saved key and the status falls back to the install-time env var.
func TestDeleteAPIKeyFallsBackToEnv(t *testing.T) {
	t.Setenv("ARRMADA_TMDB_API_KEY", "fromenv1a2b")
	s := newKeyServer(t)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)
	if rec := s.doBody("PUT", "/api/v1/apikeys/tmdb", `{"value":"saved9z9z"}`, mgr); rec.Code != http.StatusOK {
		t.Fatalf("save: HTTP %d %s", rec.Code, rec.Body)
	}
	if rec := s.do("DELETE", "/api/v1/apikeys/tmdb", kid); rec.Code != http.StatusForbidden {
		t.Errorf("requester clear: HTTP %d, want 403", rec.Code)
	}

	rec := s.do("DELETE", "/api/v1/apikeys/tmdb", mgr)
	if rec.Code != http.StatusOK {
		t.Fatalf("clear: HTTP %d %s", rec.Code, rec.Body)
	}
	st := keyStatus(t, rec, "tmdb")
	if !st.Configured || st.Source != "env" || !st.EnvSet || st.Hint != "…1a2b" {
		t.Errorf("after clear: %+v, want configured from env", st)
	}
	if got := s.deps.APIKeys.Value(t.Context(), "tmdb"); got != "fromenv1a2b" {
		t.Errorf("effective key = %q, want the env value", got)
	}
	if rec := s.do("DELETE", "/api/v1/apikeys/nope", mgr); rec.Code != http.StatusNotFound {
		t.Errorf("unknown id: HTTP %d, want 404", rec.Code)
	}
}
