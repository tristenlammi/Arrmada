package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/flaresolverr"
)

// The status endpoint says "not set up" without a URL and "ready" with a live one; the
// API-keys Test reports the version or the exact failure.
func TestFlareSolverrStatusAndTest(t *testing.T) {
	get := func(a *api) map[string]any {
		w := httptest.NewRecorder()
		a.handleFlareSolverrStatus(w, httptest.NewRequest(http.MethodGet, "/api/v1/flaresolverr/status", nil))
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	if st := get(&api{}); st["configured"] != false || st["ok"] != false {
		t.Fatalf("no client: %+v", st)
	}

	live := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"status":"ok","version":"3.3.21"}`)
	}))
	defer live.Close()
	a := &api{deps: Deps{FlareSolverr: flaresolverr.New(live.URL)}}
	if st := get(a); st["configured"] != true || st["ok"] != true || st["version"] != "3.3.21" {
		t.Fatalf("live: %+v", st)
	}

	test := func(a *api) map[string]any {
		w := httptest.NewRecorder()
		r := httptest.NewRequest(http.MethodPost, "/api/v1/apikeys/flaresolverr/test", nil)
		r.SetPathValue("id", "flaresolverr")
		a.handleTestAPIKey(w, r)
		var out map[string]any
		_ = json.Unmarshal(w.Body.Bytes(), &out)
		return out
	}
	if got := test(a); got["ok"] != true || got["detail"] != "FlareSolverr 3.3.21 is answering." {
		t.Fatalf("test live: %+v", got)
	}
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	if got := test(&api{deps: Deps{FlareSolverr: flaresolverr.New(deadURL)}}); got["ok"] != false || !strings.Contains(got["detail"].(string), "isn't answering") {
		t.Fatalf("test dead: %+v", got)
	}
	if got := test(&api{}); got["ok"] != false || !strings.Contains(got["detail"].(string), "isn't set up") {
		t.Fatalf("test unset: %+v", got)
	}
}
