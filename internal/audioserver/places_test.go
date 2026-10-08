package audioserver

import (
	"context"
	"encoding/json"
	"testing"
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
