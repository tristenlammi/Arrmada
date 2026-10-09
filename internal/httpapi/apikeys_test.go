package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/apikeys"
	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/metadata"
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
	_, mgr := s.user(t, "admin@example.com", auth.RoleAdmin) // API keys are admin-only (SEC-09)
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

// Test dispatches to the wired verifier, passing a typed-but-unsaved value through as the
// candidate without storing it; unknown ids and a Hardcover candidate are 400s.
func TestTestAPIKeyDispatchesAndNeverStoresCandidate(t *testing.T) {
	var gotCandidate []string
	s := newRouteServer(t, func(d *Deps) {
		d.APIKeys = apikeys.NewStore(d.Settings)
		d.KeyVerifiers = map[string]func(context.Context, string) (string, error){
			"omdb": func(_ context.Context, candidate string) (string, error) {
				gotCandidate = append(gotCandidate, candidate)
				if candidate == "bad" {
					return "", errors.New("OMDb: Invalid API key!")
				}
				return "OK: OMDb answered.", nil
			},
			"tvdb": func(context.Context, string) (string, error) { return "", metadata.ErrNotConfigured },
		}
	})
	_, admin := s.user(t, "admin@example.com", auth.RoleAdmin)
	_, mgr := s.user(t, "mgr@example.com", auth.RoleManager)
	if rec := s.doBody("PUT", "/api/v1/apikeys/omdb", `{"value":"saved-omdb"}`, admin); rec.Code != http.StatusOK {
		t.Fatalf("save: HTTP %d %s", rec.Code, rec.Body)
	}

	result := func(rec *httptest.ResponseRecorder) (bool, string) {
		t.Helper()
		var out struct {
			OK     bool   `json:"ok"`
			Detail string `json:"detail"`
		}
		if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &out) != nil {
			t.Fatalf("test: HTTP %d %s", rec.Code, rec.Body)
		}
		return out.OK, out.Detail
	}

	// No body: the saved key.
	if ok, detail := result(s.do("POST", "/api/v1/apikeys/omdb/test", admin)); !ok || detail != "OK: OMDb answered." {
		t.Errorf("saved: %v %q", ok, detail)
	}
	// A candidate is tested and reported, and the saved key is unchanged afterwards.
	if ok, detail := result(s.doBody("POST", "/api/v1/apikeys/omdb/test", `{"value":"  bad "}`, admin)); ok || detail != "OMDb: Invalid API key!" {
		t.Errorf("candidate: %v %q", ok, detail)
	}
	if strings.Join(gotCandidate, ",") != ",bad" {
		t.Errorf("verifier saw candidates %q, want the saved run then the trimmed candidate", gotCandidate)
	}
	if got := s.deps.APIKeys.Value(t.Context(), "omdb"); got != "saved-omdb" {
		t.Errorf("stored key after a candidate test = %q, want it unchanged", got)
	}
	if ok, detail := result(s.do("POST", "/api/v1/apikeys/tvdb/test", admin)); ok || !strings.Contains(detail, "no key is set") {
		t.Errorf("unconfigured: %v %q", ok, detail)
	}

	for _, path := range []string{"/api/v1/apikeys/nope/test", "/api/v1/apikeys/opensubtitles_password/test"} {
		if rec := s.do("POST", path, admin); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400", path, rec.Code)
		}
	}
	if rec := s.doBody("POST", "/api/v1/apikeys/hardcover/test", `{"value":"x"}`, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("hardcover candidate: HTTP %d, want 400", rec.Code)
	}
	if rec := s.doBody("POST", "/api/v1/apikeys/omdb/test", `{"value":"x","extra":1}`, admin); rec.Code != http.StatusBadRequest {
		t.Errorf("unknown field: HTTP %d, want 400", rec.Code)
	}
	if rec := s.doBody("POST", "/api/v1/apikeys/omdb/test", `{"value":"x"}`, mgr); rec.Code != http.StatusForbidden {
		t.Errorf("manager: HTTP %d, want 403 (API keys are admin-only)", rec.Code)
	}
}

// DELETE clears the saved key and the status falls back to the install-time env var.
func TestDeleteAPIKeyFallsBackToEnv(t *testing.T) {
	t.Setenv("ARRMADA_TMDB_API_KEY", "fromenv1a2b")
	s := newKeyServer(t)
	_, mgr := s.user(t, "admin@example.com", auth.RoleAdmin) // API keys are admin-only (SEC-09)
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
