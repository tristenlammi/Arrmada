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
		`{"plex_login_auto_approve_types":"movie,series"}`,
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

// Book languages default to English, are stored as canonical codes whatever spelling was
// sent, may be emptied (any language), and refuse anything that isn't a language. [BOOK-16]
func TestSettingsBookLanguages(t *testing.T) {
	a := settingsAPI(t)
	if _, got := getSettings(t, a); !reflect.DeepEqual(got["books_languages"], []any{"en"}) {
		t.Fatalf("default = %v, want [en]", got["books_languages"])
	}
	if w := putSettingsAs(t, a, auth.RoleManager, `{"books_languages":["English","SPA","es"]}`); w.Code != http.StatusOK {
		t.Fatalf("manager PUT: HTTP %d %s", w.Code, w.Body.String())
	}
	if _, got := getSettings(t, a); !reflect.DeepEqual(got["books_languages"], []any{"en", "es"}) {
		t.Errorf("saved = %v, want [en es]", got["books_languages"])
	}
	if w := putSettings(t, a, `{"books_languages":["en","klingonish"]}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown language: HTTP %d, want 400", w.Code)
	}
	if _, got := getSettings(t, a); !reflect.DeepEqual(got["books_languages"], []any{"en", "es"}) {
		t.Errorf("a refused PUT changed the list: %v", got["books_languages"])
	}
	if w := putSettings(t, a, `{"books_languages":[]}`); w.Code != http.StatusOK {
		t.Fatalf("empty list: HTTP %d %s", w.Code, w.Body.String())
	}
	if _, got := getSettings(t, a); !reflect.DeepEqual(got["books_languages"], []any{}) {
		t.Errorf("emptied = %v, want [] (any language)", got["books_languages"])
	}
}

// "Also match titles in" is empty by default (English and original titles only), saved as
// canonical codes by a manager, and refuses anything that isn't a language.
func TestSettingsTitleLanguages(t *testing.T) {
	a := settingsAPI(t)
	if _, got := getSettings(t, a); !reflect.DeepEqual(got["match_title_languages"], []any{}) {
		t.Fatalf("default = %v, want []", got["match_title_languages"])
	}
	if w := putSettingsAs(t, a, auth.RoleManager, `{"match_title_languages":["German","fre","de"]}`); w.Code != http.StatusOK {
		t.Fatalf("manager PUT: HTTP %d %s", w.Code, w.Body.String())
	}
	if _, got := getSettings(t, a); !reflect.DeepEqual(got["match_title_languages"], []any{"de", "fr"}) {
		t.Errorf("saved = %v, want [de fr]", got["match_title_languages"])
	}
	if w := putSettings(t, a, `{"match_title_languages":["de","klingonish"]}`); w.Code != http.StatusBadRequest {
		t.Errorf("unknown language: HTTP %d, want 400", w.Code)
	}
	if w := putSettings(t, a, `{"match_title_languages":[]}`); w.Code != http.StatusOK {
		t.Fatalf("empty list: HTTP %d %s", w.Code, w.Body.String())
	}
	if _, got := getSettings(t, a); !reflect.DeepEqual(got["match_title_languages"], []any{}) {
		t.Errorf("emptied = %v, want []", got["match_title_languages"])
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

// Settings save one control at a time now, so every key PUT /settings accepts must save on
// its own: 200, that key changed, nothing else touched. The keys come from settingsUpdate's
// tags, so a new setting without a row here fails the test rather than going unchecked.
func TestUpdateSettingsSingleKey(t *testing.T) {
	values := map[string]string{
		"series_monitor_default":            `"future"`,
		"naming_movie_folder":               `"{title}"`,
		"naming_movie_file":                 `"{title} - {quality}"`,
		"naming_series_folder":              `"{title} [{year}]"`,
		"naming_series_season":              `"S{season00}"`,
		"naming_series_episode":             `"{title} {episode}"`,
		"naming_audiobook_folder":           `"{Author}/{Series}/{Title}"`,
		"naming_audiobook_file":             `"{Author} - {Title}"`,
		"naming_audiobook_track":            `"{Title} {Part}"`,
		"naming_ebook_folder":               `"{Author}/{Title} ({Year})"`,
		"naming_ebook_file":                 `"{Title} - {Author}"`,
		"naming_music_folder":               `"{Album Artist}/{Original Year} - {Album Title}"`,
		"naming_music_track":                `"{Track:00}. {Track Title}"`,
		"naming_music_compilation":          `"{Track:00}. {Track Artist} - {Track Title}"`,
		"music_extras":                      `".jpg .pdf .lrc"`,
		"tmdb_region":                       `"AU"`,
		"plex_login_auto_approve":           `true`,
		"plex_login_auto_approve_types":     `"movie,series"`,
		"jellyfin_login_auto_approve_types": `"series,book"`,
		"recycle_max_gb":                    `"7"`,
		"recycle_retention_days":            `"9"`,
		"downloads_disk_guard_pause_pct":    `"90"`,
		"downloads_disk_guard_resume_pct":   `"70"`,
		// A real path needs a running guard and a second filesystem: diskguard_measure_test.go.
		"downloads_disk_guard_measure_path":  `""`,
		"downloads_disk_guard_pause_free_gb": `"50"`,
		"downloads_stall_minutes":            `30`,
		"upgrade_max_grabs_per_sweep":        `25`,
		"request_quota_days":                 `14`,
		"request_quota_movies":               `3`,
		"request_quota_seasons":              `2`,
		"request_quota_books":                `4`,
		"books_languages":                    `["en","es"]`,
		"match_title_languages":              `["de"]`,
		"display_title_mode":                 `"original"`,
		"display_title_language":             `"el"`,
		"display_title_original_in":          `["el","it"]`,
		"quality_ranking":                    `"v2"`,
		"download_network_preference":        `"usenet"`,
		"app_public_url":                     `"https://arrmada.example.com"`,
		"seeding_global_ratio_limit":         `2.5`,
		"seeding_global_max_time_hours":      `168`,
		"seeding_global_limit_action":        `"stop"`,
	}
	// The legacy all-types switch and the per-type list are two views of one setting.
	linked := map[string]string{"plex_login_auto_approve": "plex_login_auto_approve_types", "plex_login_auto_approve_types": "plex_login_auto_approve"}

	rt := reflect.TypeOf(settingsUpdate{})
	for i := 0; i < rt.NumField(); i++ {
		key := strings.Split(rt.Field(i).Tag.Get("json"), ",")[0]
		t.Run(key, func(t *testing.T) {
			a := settingsAPI(t)
			_, before := getSettings(t, a)
			val, ok := values[key]
			if !ok {
				b, isBool := before[key].(bool)
				if !isBool {
					t.Fatalf("no test value for %q: add one to the table", key)
				}
				val = strconv.FormatBool(!b)
			}
			if w := putSettings(t, a, `{"`+key+`":`+val+`}`); w.Code != http.StatusOK {
				t.Fatalf("PUT {%s: %s}: HTTP %d %s", key, val, w.Code, w.Body.String())
			}
			_, after := getSettings(t, a)
			var want any
			_ = json.Unmarshal([]byte(val), &want)
			if !reflect.DeepEqual(after[key], want) {
				t.Errorf("%s = %v after saving %v", key, after[key], want)
			}
			for k, v := range before {
				if k != key && k != linked[key] && !reflect.DeepEqual(after[k], v) {
					t.Errorf("%s changed from %v to %v though only %s was sent", k, v, after[k], key)
				}
			}
		})
	}
}

// The disk guard's pause and resume points are checked as a pair whichever one is sent:
// one alone is judged against the stored other, both together against each other.
func TestDiskGuardPairValidatedTogether(t *testing.T) {
	a := settingsAPI(t) // defaults: pause 85, resume 80
	if w := putSettings(t, a, `{"downloads_disk_guard_pause_pct":"70"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("pause 70 below the stored resume 80: HTTP %d, want 400", w.Code)
	}
	if w := putSettings(t, a, `{"downloads_disk_guard_pause_pct":"70","downloads_disk_guard_resume_pct":"60"}`); w.Code != http.StatusOK {
		t.Fatalf("70/60 together: HTTP %d %s", w.Code, w.Body.String())
	}
	if w := putSettings(t, a, `{"downloads_disk_guard_resume_pct":"75"}`); w.Code != http.StatusBadRequest {
		t.Errorf("resume 75 above the stored pause 70: HTTP %d, want 400", w.Code)
	}
	if w := putSettings(t, a, `{"downloads_disk_guard_resume_pct":"65"}`); w.Code != http.StatusOK {
		t.Errorf("resume 65 under pause 70: HTTP %d %s", w.Code, w.Body.String())
	}
	for _, bad := range []string{`""`, `"abc"`, `"8 5"`, `"-5"`} {
		if w := putSettings(t, a, `{"downloads_disk_guard_pause_pct":`+bad+`}`); w.Code != http.StatusBadRequest {
			t.Errorf("pause %s: HTTP %d, want 400", bad, w.Code)
		}
	}
	if _, got := getSettings(t, a); got["downloads_disk_guard_pause_pct"] != "70" || got["downloads_disk_guard_resume_pct"] != "65" {
		t.Errorf("stored pair = %v/%v, want 70/65", got["downloads_disk_guard_pause_pct"], got["downloads_disk_guard_resume_pct"])
	}
}

// A field emptied and left (auto-save sends it on blur) must not become a different
// setting: a blank bin limit would read as unlimited, a blank template as the default.
func TestSettingsRefuseBlankValues(t *testing.T) {
	a := settingsAPI(t)
	for _, body := range []string{
		`{"recycle_max_gb":""}`, `{"recycle_retention_days":" "}`, `{"recycle_max_gb":"1.5"}`, `{"recycle_max_gb":"-1"}`,
		`{"naming_movie_folder":""}`, `{"naming_series_episode":"   "}`,
	} {
		if w := putSettings(t, a, body); w.Code != http.StatusBadRequest {
			t.Errorf("%s: HTTP %d, want 400", body, w.Code)
		}
	}
	if w := putSettings(t, a, `{"recycle_max_gb":"0","recycle_retention_days":"0"}`); w.Code != http.StatusOK {
		t.Errorf("0 (unlimited / forever): HTTP %d %s", w.Code, w.Body.String())
	}
}
