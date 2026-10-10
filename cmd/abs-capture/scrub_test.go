package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A capture keeps its shape and loses everything that names the server or the person.
func TestScrub(t *testing.T) {
	s := newScrubber("Abs.Home.Lan", "Tristen")
	in := map[string]any{
		"user": map[string]any{
			"id": "0f9a3a1c-1111-4c3e-9d8e-2b7f5a6c7d8e", "username": "Tristen", "email": "t@home.lan",
			"token": "abc", "accessToken": "eyJhbGciOiJIUzI1NiJ9.eyJ1c2VySWQiOiIxIn0.sig", "refreshToken": nil,
			"type": "user",
		},
		"note":       "token eyJhbGciOiJIUzI1NiJ9.eyJ1c2VySWQiOiIxIn0.c2lnbmF0dXJl inside text",
		"deviceInfo": map[string]any{"ipAddress": "10.0.0.7", "clientName": "Tristen's iPhone"},
		"link":       "http://abs.home.lan:13378/api/items/x/cover",
		"seen":       "from 192.168.1.20 and fe80::1c2b:3cff:fe4d:5e6f",
		"path":       "/mnt/user/media/Audiobooks/H. G. Wells/The Time Machine/01.mp3",
		"winPath":    `D:\Books\The Time Machine\cover.jpg`,
		"cover":      "/api/items/x/cover",
		"duration":   12.5,
		"clock":      "at 12:34:56",
		"list":       []any{"Abs.home.lan", 3.0},
	}
	out := s.value("", in).(map[string]any)
	b, _ := json.Marshal(out)
	got := string(b)
	for _, leak := range []string{"Tristen", "tristen", "home.lan", "10.0.0.7", "192.168.1.20", "fe80", "eyJ", "abc", "/mnt/user", `D:\`, "H. G. Wells"} {
		if strings.Contains(got, leak) {
			t.Errorf("scrubbed capture still holds %q: %s", leak, got)
		}
	}
	u := out["user"].(map[string]any)
	if u["token"] != redacted || u["accessToken"] != redacted || u["refreshToken"] != nil {
		t.Errorf("tokens: %v %v %v", u["token"], u["accessToken"], u["refreshToken"])
	}
	if u["username"] != "listener" || u["type"] != "user" || u["id"] != "0f9a3a1c-1111-4c3e-9d8e-2b7f5a6c7d8e" {
		t.Errorf("user: %v", u)
	}
	if out["path"] != "/audiobooks/Book/01.mp3" || out["winPath"] != "/audiobooks/Book/cover.jpg" {
		t.Errorf("paths: %v %v", out["path"], out["winPath"])
	}
	if out["cover"] != "/api/items/x/cover" {
		t.Errorf("an API route was rewritten: %v", out["cover"])
	}
	if out["duration"] != 12.5 || out["deviceInfo"].(map[string]any)["ipAddress"] != "192.0.2.1" {
		t.Errorf("kinds or ipAddress: %v %v", out["duration"], out["deviceInfo"])
	}
	if out["clock"] != "at 12:34:56" {
		t.Errorf("a clock time was taken for an address: %v", out["clock"])
	}
	if out["link"] != "http://abs.example/api/items/x/cover" {
		t.Errorf("link: %v", out["link"])
	}
}

// The recorded request bodies keep their placeholders: the password never reaches
// steps.json.
func TestConversationBodiesArePlaceholders(t *testing.T) {
	b, _ := json.Marshal(conversation())
	if !strings.Contains(string(b), `"password":"{password}"`) {
		t.Fatal("login body lost its placeholder")
	}
	// run() fills each step's body for the request and then writes the same steps out:
	// filling must not touch the template.
	steps := conversation()
	sent, _ := json.Marshal(fillAny(steps[2].Body, map[string]string{"password": "hunter2"}))
	kept, _ := json.Marshal(steps)
	if !strings.Contains(string(sent), "hunter2") || strings.Contains(string(kept), "hunter2") {
		t.Fatalf("filling a request changed the recorded template: sent %s", sent)
	}
}

// The conversation the capture records is the one the fixtures hold, step for step, so a
// re-capture replaces them like for like.
func TestConversationMatchesFixtures(t *testing.T) {
	dir := filepath.Join("..", "..", "internal", "audioserver", "testdata", "abs")
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, st := range conversation() {
		names = append(names, st.Name)
	}
	for _, e := range ents {
		if !e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name(), "steps.json"))
		if err != nil {
			t.Fatal(err)
		}
		var rec struct {
			Steps []step `json:"steps"`
		}
		if err := json.Unmarshal(b, &rec); err != nil {
			t.Fatal(err)
		}
		var got []string
		for _, st := range rec.Steps {
			got = append(got, st.Name)
		}
		if strings.Join(got, ",") != strings.Join(names, ",") {
			t.Errorf("%s/steps.json has steps\n%v\nthe capture records\n%v", e.Name(), got, names)
		}
	}
}
