package audioserver

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

// Places an app can't be trusted to send correctly: a close with no position, numbers
// as strings. Whatever arrives, the saved place must only move when it should.

// signIn signs the reader in with their audiobook password and keeps the token.
func (h *harness) signIn() {
	h.t.Helper()
	_, out := h.do("POST", "/login", map[string]string{"username": "reader", "password": "listen-pass-1"}, nil)
	var login map[string]any
	_ = json.Unmarshal(out, &login)
	h.token = obj1(h.t, login["user"])["accessToken"].(string)
}

func (h *harness) readerID() int64 {
	h.t.Helper()
	u, err := h.users.UserByUsername(context.Background(), "reader")
	if err != nil {
		h.t.Fatal(err)
	}
	return u.ID
}

// play opens a play session for the item and returns its id.
func (h *harness) play(key string) string {
	h.t.Helper()
	return h.json("POST", "/api/items/"+key+"/play", map[string]any{
		"deviceInfo": map[string]string{"clientName": "Lissen", "deviceId": "dev-1", "deviceName": "Pixel 8"},
	})["id"].(string)
}

// A close (or sync) that doesn't say where the app is never moves the place: not near
// the start, where it used to reset to 0:00, and not deep in, where it used to hold a
// "jump back to 0:00" for the person to wrongly confirm.
func TestCloseWithoutBodyKeepsPlace(t *testing.T) {
	ctx := context.Background()
	for _, saved := range []float64{120, 90, 3600} {
		h := newHarness(t)
		h.signIn()
		key := itemKeyFor(h.book.ID, 0)
		for _, body := range []any{nil, json.RawMessage("null"), json.RawMessage("{}"),
			json.RawMessage(`{"currentTime":null,"timeListened":5}`), json.RawMessage(`{"currentTime":""}`)} {
			sid := h.play(key)
			if code, out := h.do("POST", "/api/session/"+sid+"/sync", map[string]any{"currentTime": saved, "timeListened": 0}, nil); code != 200 {
				t.Fatalf("sync: HTTP %d %s", code, out)
			}
			if code, out := h.do("POST", "/api/session/"+sid+"/close", body, nil); code != 200 {
				t.Fatalf("close with %s: HTTP %d %s", body, code, out)
			}
			if got := h.json("GET", "/api/me/progress/"+key, nil)["currentTime"].(float64); got != saved {
				t.Fatalf("saved %v: close with %s moved the place to %v", saved, body, got)
			}
			p, _, _ := h.srv.listen.Progress(ctx, h.readerID(), key)
			if p.PendingPosition != nil || p.Finished {
				t.Fatalf("saved %v: close with %s left %+v", saved, body, p)
			}
			sess, _ := h.srv.listen.GetSession(ctx, h.readerID(), sid)
			if !sess.Closed || sess.CurPos != saved {
				t.Fatalf("saved %v: session after close with %s = %+v", saved, body, sess)
			}
		}
	}
}

// "Listen again": playing a finished book starts from 0:00, and listening on from there
// saves it. Opening and closing it without listening leaves it finished.
func TestListenAgainStartsFromZero(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	key := itemKeyFor(h.book.ID, 0)
	finished := func() map[string]any {
		return h.json("PATCH", "/api/me/progress/"+key, map[string]any{"isFinished": true, "duration": 36000})
	}
	if p := finished(); p["isFinished"] != true {
		t.Fatalf("mark finished: %v", p)
	}

	// Tap and close.
	sid := h.play(key)
	if code, out := h.do("POST", "/api/session/"+sid+"/close", nil, nil); code != 200 {
		t.Fatalf("close: HTTP %d %s", code, out)
	}
	if p := h.json("GET", "/api/me/progress/"+key, nil); p["isFinished"] != true {
		t.Fatalf("tap and close unfinished the book: %v", p)
	}
	code, out := h.do("GET", "/api/libraries/"+libraryID+"/personalized", nil, nil)
	var shelves []map[string]any
	if code != 200 || json.Unmarshal(out, &shelves) != nil || shelves[len(shelves)-1]["id"] != "listen-again" {
		t.Fatalf("the book left Listen Again: %s", out)
	}

	play := h.json("POST", "/api/items/"+key+"/play", map[string]any{"deviceInfo": map[string]string{"clientName": "Lissen", "deviceName": "Pixel 8"}})
	if play["startTime"] != 0.0 || play["currentTime"] != 0.0 {
		t.Fatalf("play started at %v / %v, want 0", play["startTime"], play["currentTime"])
	}
	ump := obj1(t, obj1(t, play["libraryItem"])["userMediaProgress"])
	if ump["currentTime"] != 0.0 || ump["isFinished"] != false {
		t.Fatalf("play's userMediaProgress = %v, want 0:00 unfinished", ump)
	}
	sid = play["id"].(string)
	for _, pos := range []float64{15, 30} {
		if code, out := h.do("POST", "/api/session/"+sid+"/sync", map[string]any{"currentTime": pos, "timeListened": 15}, nil); code != 200 {
			t.Fatalf("sync: HTTP %d %s", code, out)
		}
	}
	if p := h.json("GET", "/api/me/progress/"+key, nil); p["isFinished"] != false || p["currentTime"] != 30.0 {
		t.Fatalf("after listening again for 30 s: %v", p)
	}
}

// A progress PATCH from an app goes through the same guards as a play session instead
// of always winning: an older copy is ignored, a big jump back is held until playback
// carries on from it, and the reply is the place actually kept.
func TestPatchProgressRespectsGuards(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	ctx := context.Background()
	key := itemKeyFor(h.book.ID, 0)
	path := "/api/me/progress/" + key
	place := func() float64 { return h.json("GET", path, nil)["currentTime"].(float64) }

	h.json("PATCH", path, map[string]any{"currentTime": 3000, "duration": 36000})
	if got := place(); got != 3000 {
		t.Fatalf("first PATCH saved %v", got)
	}

	// A stale copy, set ten minutes before the saved place.
	old := time.Now().Add(-10 * time.Minute).UnixMilli()
	if r := h.json("PATCH", path, map[string]any{"currentTime": 5000, "lastUpdate": old}); r["currentTime"] != 3000.0 {
		t.Fatalf("older PATCH reply = %v, want the kept place", r["currentTime"])
	}
	if got := place(); got != 3000 {
		t.Fatalf("older PATCH moved the place to %v", got)
	}

	// Far back: held, not saved, until a play session carries on from there for 30 s.
	if r := h.json("PATCH", path, map[string]any{"currentTime": "100"}); r["currentTime"] != 3000.0 {
		t.Fatalf("held PATCH reply = %v, want the kept place", r["currentTime"])
	}
	p, _, _ := h.srv.listen.Progress(ctx, h.readerID(), key)
	if p.Position != 3000 || p.PendingPosition == nil || *p.PendingPosition != 100 {
		t.Fatalf("after a far-back PATCH: %+v", p)
	}
	sid := h.play(key)
	for i, pos := range []float64{110, 125, 140} {
		if code, out := h.do("POST", "/api/session/"+sid+"/sync", map[string]any{"currentTime": pos, "timeListened": []float64{10, 15, 15}[i]}, nil); code != 200 {
			t.Fatalf("sync: HTTP %d %s", code, out)
		}
	}
	if got := place(); got != 140 {
		t.Fatalf("after playing on from the held spot the place is %v, want 140", got)
	}

	// Forward, and a small skip back, apply at once.
	h.json("PATCH", path, map[string]any{"currentTime": 600})
	h.json("PATCH", path, map[string]any{"progress": 500.0 / 36000})
	if got := place(); got != 500 {
		t.Fatalf("forward then a small skip back left %v, want 500", got)
	}

	// Finished and not finished are the person's own actions.
	if r := h.json("PATCH", path, map[string]any{"isFinished": true}); r["isFinished"] != true {
		t.Fatalf("mark finished: %v", r)
	}
	end := place()
	if r := h.json("PATCH", path, map[string]any{"isFinished": false}); r["isFinished"] != false || r["currentTime"] != end {
		t.Fatalf("mark not finished: %v, want unfinished at %v", r, end)
	}

	// The batch route applies the same rules to each entry.
	code, out := h.do("PATCH", "/api/me/progress/batch/update", []map[string]any{
		{"libraryItemId": key, "currentTime": 10},
	}, nil)
	if code != 200 {
		t.Fatalf("batch: HTTP %d %s", code, out)
	}
	if got := place(); got != end {
		t.Fatalf("a far-back batch entry moved the place to %v", got)
	}
	if p, _, _ := h.srv.listen.Progress(ctx, h.readerID(), key); p.PendingPosition == nil || *p.PendingPosition != 10 {
		t.Fatalf("batch entry not held: %+v", p)
	}
	h.do("PATCH", "/api/me/progress/batch/update", []map[string]any{{"libraryItemId": key, "currentTime": end - 30, "lastUpdate": old}}, nil)
	if got := place(); got != end {
		t.Fatalf("an older batch entry moved the place to %v", got)
	}
	h.do("PATCH", "/api/me/progress/batch/update", []map[string]any{{"libraryItemId": key, "currentTime": end - 60}}, nil)
	if got := place(); got != end-60 {
		t.Fatalf("a small skip back in a batch left %v, want %v", got, end-60)
	}
}

// Numbers sent as strings are read as numbers, like Audiobookshelf reads them.
func TestSyncNumericStringsAccepted(t *testing.T) {
	h := newHarness(t)
	h.signIn()
	key := itemKeyFor(h.book.ID, 0)
	sid := h.play(key)
	if code, out := h.do("POST", "/api/session/"+sid+"/sync", json.RawMessage(`{"currentTime":"150","timeListened":"30"}`), nil); code != 200 {
		t.Fatalf("sync: HTTP %d %s", code, out)
	}
	if got := h.json("GET", "/api/me/progress/"+key, nil)["currentTime"].(float64); got != 150 {
		t.Fatalf("place after a string currentTime = %v, want 150", got)
	}
	if sess, _ := h.srv.listen.GetSession(context.Background(), h.readerID(), sid); sess.Listened <= 0 {
		t.Fatalf("string timeListened not counted: %+v", sess)
	}
}
