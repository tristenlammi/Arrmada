package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

func settingsAPI(t *testing.T) *api {
	t.Helper()
	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return &api{deps: Deps{Settings: settings.NewService(st.DB())}}
}

func getSettings(t *testing.T, a *api) (string, map[string]any) {
	t.Helper()
	w := httptest.NewRecorder()
	a.handleGetSettings(w, httptest.NewRequest(http.MethodGet, "/", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("GET /settings returned %d: %s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decoding %q: %v", w.Body.String(), err)
	}
	return w.Body.String(), got
}

func putSettings(t *testing.T, a *api, body string) *httptest.ResponseRecorder {
	t.Helper()
	return putSettingsAs(t, a, auth.RoleAdmin, body)
}

func putSettingsAs(t *testing.T, a *api, role auth.Role, body string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	r := withUser(httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body)), &auth.User{ID: 2, Username: "someone", Role: role})
	a.handleUpdateSettings(w, r)
	return w
}

// A manager runs the media day to day but the module switches, Plex sign-in, the
// Discovery region and the bin and disk guard limits are the admin's. The Settings page
// can send the whole object back, so an admin field resent unchanged must still save.
func TestManagerCannotChangeAdminSettings(t *testing.T) {
	a := settingsAPI(t)

	w := putSettingsAs(t, a, auth.RoleManager, `{"plex_login_enabled":true}`)
	if w.Code != http.StatusForbidden || !strings.Contains(w.Body.String(), "Plex sign-in") {
		t.Fatalf("manager turning Plex sign-in on: HTTP %d %s, want a 403 naming the field", w.Code, w.Body.String())
	}
	if _, got := getSettings(t, a); got["plex_login_enabled"] != false {
		t.Fatal("the refused change was saved")
	}

	// The same value sent back, alongside a manager field, saves.
	if w := putSettingsAs(t, a, auth.RoleManager, `{"plex_login_enabled":false,"search_on_add":false}`); w.Code != http.StatusOK {
		t.Fatalf("unchanged resend: HTTP %d %s", w.Code, w.Body.String())
	}
	if _, got := getSettings(t, a); got["search_on_add"] != false {
		t.Error("search_on_add did not save for a manager")
	}

	// The whole GET body echoed back (defaults included) is not a change.
	raw, _ := getSettings(t, a)
	if w := putSettingsAs(t, a, auth.RoleManager, raw); w.Code != http.StatusOK {
		t.Fatalf("full resend: HTTP %d %s", w.Code, w.Body.String())
	}
	// A lower-case region equal to the stored one isn't a change either.
	if w := putSettings(t, a, `{"tmdb_region":"AU"}`); w.Code != http.StatusOK {
		t.Fatalf("admin setting the region: HTTP %d", w.Code)
	}
	if w := putSettingsAs(t, a, auth.RoleManager, `{"tmdb_region":" au "}`); w.Code != http.StatusOK {
		t.Errorf("same region in another case: HTTP %d %s", w.Code, w.Body.String())
	}

	// A mixed body with one forbidden change saves nothing, the allowed keys included.
	w = putSettingsAs(t, a, auth.RoleManager, `{"write_nfo":true,"search_on_add":true,"recycle_max_gb":"1"}`)
	if w.Code != http.StatusForbidden {
		t.Fatalf("mixed body: HTTP %d, want 403", w.Code)
	}
	if _, got := getSettings(t, a); got["write_nfo"] != false || got["search_on_add"] != false {
		t.Errorf("a refused body saved its other keys: %v", got)
	}

	for _, body := range []string{
		`{"books_enabled":false}`, `{"music_enabled":true}`, `{"plex_login_auto_approve":false}`,
		`{"tmdb_region":"US"}`, `{"recycle_retention_days":"1"}`, `{"downloads_disk_guard":false}`,
		`{"downloads_disk_guard_pause_pct":"80"}`, `{"downloads_disk_guard_resume_pct":"5"}`,
	} {
		for _, role := range []auth.Role{auth.RoleManager, auth.RoleRequester, ""} {
			if w := putSettingsAs(t, a, role, body); w.Code != http.StatusForbidden {
				t.Errorf("%s as %q: HTTP %d, want 403", body, role, w.Code)
			}
		}
	}

	// Managers keep their own fields, and an admin can change anything.
	for _, body := range []string{`{"write_nfo":true}`, `{"naming_movie_file":"{title}"}`, `{"downloads_stall_minutes":30}`} {
		if w := putSettingsAs(t, a, auth.RoleManager, body); w.Code != http.StatusOK {
			t.Errorf("manager %s: HTTP %d %s", body, w.Code, w.Body.String())
		}
	}
	if w := putSettings(t, a, `{"plex_login_enabled":true,"music_enabled":true}`); w.Code != http.StatusOK {
		t.Errorf("admin: HTTP %d %s", w.Code, w.Body.String())
	}
}

// A bad region is refused before any other key in the body is written.
func TestSettingsValidatesBeforeWriting(t *testing.T) {
	a := settingsAPI(t)
	if w := putSettings(t, a, `{"write_nfo":true,"tmdb_region":"Australia"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("HTTP %d, want 400", w.Code)
	}
	if _, got := getSettings(t, a); got["write_nfo"] != false {
		t.Error("write_nfo saved though the body was refused")
	}
}

// Whatever GET /settings returns must be accepted by PUT /settings unchanged. The page
// saves by sending values back, so a read-only field in the GET (server_time/server_tz
// did exactly this) turns every Save into a 400 — this test catches the next one.
func TestSettingsGetPutRoundTrip(t *testing.T) {
	a := settingsAPI(t)
	raw, before := getSettings(t, a)

	w := putSettings(t, a, raw)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT of the GET body returned %d: %s", w.Code, w.Body.String())
	}
	var saved map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &saved); err != nil {
		t.Fatalf("decoding %q: %v", w.Body.String(), err)
	}
	if !reflect.DeepEqual(saved, before) {
		t.Errorf("PUT response differs from the GET it echoed:\n got %v\nwant %v", saved, before)
	}
	if _, after := getSettings(t, a); !reflect.DeepEqual(after, before) {
		t.Errorf("round trip changed stored values:\n got %v\nwant %v", after, before)
	}
}

// A PUT carrying one key changes that key and leaves everything else as it was — the
// page now sends only what was edited, so an omitted key must never be blanked.
func TestSettingsPartialUpdate(t *testing.T) {
	a := settingsAPI(t)
	_, before := getSettings(t, a)
	if before["write_nfo"] != false {
		t.Fatalf("write_nfo should default to false, got %v", before["write_nfo"])
	}

	if w := putSettings(t, a, `{"write_nfo":true}`); w.Code != http.StatusOK {
		t.Fatalf("PUT returned %d: %s", w.Code, w.Body.String())
	}
	_, after := getSettings(t, a)
	if after["write_nfo"] != true {
		t.Errorf("write_nfo did not persist: %v", after["write_nfo"])
	}
	for k, v := range before {
		if k == "write_nfo" {
			continue
		}
		if !reflect.DeepEqual(after[k], v) {
			t.Errorf("%s changed from %v to %v though it wasn't sent", k, v, after[k])
		}
	}
}

// A key the server doesn't take is rejected by name, so the UI's error line says which.
func TestSettingsUnknownKeyNamed(t *testing.T) {
	a := settingsAPI(t)
	w := putSettings(t, a, `{"write_nfo":true,"server_time":"12:00"}`)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("got %d, want 400: %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), `unknown field \"server_time\"`) {
		t.Errorf("400 should name the field: %s", w.Body.String())
	}
	if _, got := getSettings(t, a); got["write_nfo"] != false {
		t.Error("a rejected body must not save its other keys")
	}
}

// Stall fail-over is on by default at six hours, 0 turns it off and persists, and
// anything outside 0..a week is refused rather than stored.
func TestSettingsStallMinutes(t *testing.T) {
	a := settingsAPI(t)
	if _, got := getSettings(t, a); got["downloads_stall_minutes"] != float64(360) {
		t.Fatalf("default = %v, want 360", got["downloads_stall_minutes"])
	}
	if w := putSettings(t, a, `{"downloads_stall_minutes":0}`); w.Code != http.StatusOK {
		t.Fatalf("PUT 0 returned %d: %s", w.Code, w.Body.String())
	}
	if _, got := getSettings(t, a); got["downloads_stall_minutes"] != float64(0) {
		t.Errorf("after saving 0, got %v", got["downloads_stall_minutes"])
	}
	for _, bad := range []string{"-1", "10081"} {
		if w := putSettings(t, a, `{"downloads_stall_minutes":`+bad+`}`); w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s returned %d, want 400", bad, w.Code)
		}
	}
}

// The per-sweep upgrade budget defaults to 10, round-trips (0 = no limit included), and a
// value outside 0..1000 is refused rather than stored.
func TestSettingsUpgradeBudget(t *testing.T) {
	a := settingsAPI(t)
	if _, got := getSettings(t, a); got["upgrade_max_grabs_per_sweep"] != float64(10) {
		t.Fatalf("default = %v, want 10", got["upgrade_max_grabs_per_sweep"])
	}
	for _, n := range []string{"25", "0"} {
		if w := putSettings(t, a, `{"upgrade_max_grabs_per_sweep":`+n+`}`); w.Code != http.StatusOK {
			t.Fatalf("PUT %s returned %d: %s", n, w.Code, w.Body.String())
		}
		if _, got := getSettings(t, a); got["upgrade_max_grabs_per_sweep"] != float64(mustAtoi(t, n)) {
			t.Errorf("after saving %s, got %v", n, got["upgrade_max_grabs_per_sweep"])
		}
	}
	for _, bad := range []string{"-1", "1001"} {
		if w := putSettings(t, a, `{"upgrade_max_grabs_per_sweep":`+bad+`}`); w.Code != http.StatusBadRequest {
			t.Errorf("PUT %s returned %d, want 400", bad, w.Code)
		}
	}
}

func mustAtoi(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(s)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// A profile's stall timeout is -1 (off), 0 (the default) or up to a week; anything else is
// a 400 on create and on update, before anything is stored.
func TestQualityProfileStallValidation(t *testing.T) {
	a := settingsAPI(t)
	for _, bad := range []int{-2, 10081} {
		body := `{"name":"P","media_type":"movie","stall_minutes":` + strconv.Itoa(bad) + `}`
		w := httptest.NewRecorder()
		a.handleCreateQualityProfile(w, httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body)))
		if w.Code != http.StatusBadRequest {
			t.Errorf("create with stall_minutes %d returned %d, want 400", bad, w.Code)
		}
		w = httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
		req.SetPathValue("id", "1")
		a.handleUpdateQualityProfile(w, req)
		if w.Code != http.StatusBadRequest {
			t.Errorf("update with stall_minutes %d returned %d, want 400", bad, w.Code)
		}
	}
}
