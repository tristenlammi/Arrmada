package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"testing"

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
	w := httptest.NewRecorder()
	a.handleUpdateSettings(w, httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body)))
	return w
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
