package audioserver

import (
	"encoding/json"
	"testing"
)

// Places carry the user's id: the official app's MediaProgress model requires userId and
// drops the whole reply without it.
func TestMediaProgressHasUserID(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	key := itemKeyFor(h.book.ID, 0)
	h.json("PATCH", "/api/me/progress/"+key, map[string]any{"currentTime": 120, "duration": 36000})
	want := "u" + itoa(h.readerID())
	check := func(where string, p map[string]any) {
		t.Helper()
		if p["userId"] != want {
			t.Errorf("%s: userId = %v, want %s", where, p["userId"], want)
		}
		if _, ok := p["ebookProgress"].(float64); !ok {
			t.Errorf("%s: ebookProgress = %v, want a number", where, p["ebookProgress"])
		}
		for _, k := range []string{"lastUpdate", "startedAt"} {
			if v, ok := p[k].(float64); !ok || v != float64(int64(v)) {
				t.Errorf("%s: %s = %v, want whole milliseconds (ShelfPlayer reads Int64)", where, k, p[k])
			}
		}
	}
	check("GET progress", h.json("GET", "/api/me/progress/"+key, nil))
	check("/api/me", obj1(t, list1(t, h.json("GET", "/api/me", nil)["mediaProgress"])[0]))
	check("/api/me/progress", obj1(t, list1(t, h.json("GET", "/api/me/progress", nil)["mediaProgress"])[0]))
	check("/api/authorize", obj1(t, list1(t, obj1(t, h.json("POST", "/api/authorize", nil)["user"])["mediaProgress"])[0]))
	check("item userMediaProgress", obj1(t, h.json("GET", "/api/items/"+key+"?expanded=1&include=progress", nil)["userMediaProgress"]))
}

// The long-lived token comes back in the user only to the device that signed in with it
// (ShelfPlayer can't read the user without it); a device using an access token never
// sees "token", because the official app signs itself out when it equals its token.
func TestUserTokenOnlyEchoedToLegacyDevices(t *testing.T) {
	h := newHarness(t)
	_, out := h.do("POST", "/login", map[string]string{"username": "reader", "password": "listen-pass-1"}, nil)
	var login map[string]any
	_ = json.Unmarshal(out, &login)
	u := obj1(t, login["user"])
	legacy, access := u["token"].(string), u["accessToken"].(string)
	if legacy == "" || access == "" || legacy == access {
		t.Fatalf("login tokens: %q %q", legacy, access)
	}

	h.token = access
	for _, p := range []string{"/api/authorize", "/api/me"} {
		m := h.json(map[string]string{"/api/authorize": "POST", "/api/me": "GET"}[p], p, nil)
		if p == "/api/authorize" {
			m = obj1(t, m["user"])
		}
		if _, ok := m["token"]; ok {
			t.Errorf("%s with an access token echoed a token", p)
		}
	}
	h.token = legacy
	auth := obj1(t, h.json("POST", "/api/authorize", nil)["user"])
	if auth["token"] != legacy {
		t.Errorf("authorize with the long-lived token: token = %v", auth["token"])
	}
	if me := h.json("GET", "/api/me", nil); me["token"] != legacy {
		t.Errorf("/api/me with the long-lived token: token = %v", me["token"])
	}
}

// Calls Audiobookshelf answers with a bare "OK" are answered the same way.
func TestBareOKReplies(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	key := itemKeyFor(h.book.ID, 0)
	sid := h.play(key)
	for _, c := range []struct {
		method, path string
		body         any
	}{
		{"POST", "/api/session/" + sid + "/sync", map[string]any{"currentTime": 30, "timeListened": 5}},
		{"POST", "/api/session/" + sid + "/close", nil},
		{"PATCH", "/api/me/progress/batch/update", []any{map[string]any{"libraryItemId": key, "currentTime": 40, "duration": 36000}}},
		{"POST", "/api/session/local", map[string]any{"id": "off-9", "libraryItemId": key, "currentTime": 50, "duration": 36000,
			"timeListening": 5, "startedAt": 1, "updatedAt": 2}},
		{"DELETE", "/api/me/progress/" + key, nil},
	} {
		code, out := h.do(c.method, c.path, c.body, nil)
		if code != 200 || string(out) != "OK" {
			t.Errorf("%s %s: HTTP %d %q, want 200 \"OK\"", c.method, c.path, code, out)
		}
	}
}
