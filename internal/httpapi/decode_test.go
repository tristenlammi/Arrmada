package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A body over the cap says so — an uploaded season-pack .torrent used to fail as
// "invalid request body", which sent people looking at the file instead of the size.
func TestDecodeJSONLimitReportsOversize(t *testing.T) {
	a := &api{}
	big := `{"torrent":"` + strings.Repeat("A", 2<<20) + `"}`
	r := httptest.NewRequest("POST", "/", strings.NewReader(big))
	w := httptest.NewRecorder()
	var dst torrentUpload
	if a.decodeJSON(w, r, &dst) {
		t.Fatal("2 MB body must not pass the 1 MB default cap")
	}
	if w.Code != http.StatusRequestEntityTooLarge || !strings.Contains(w.Body.String(), "too large") {
		t.Errorf("default cap: got %d %q, want 413 mentioning too large", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(big))
	w = httptest.NewRecorder()
	if !a.decodeJSONLimit(w, r, &dst, torrentBodyLimit) || len(dst.Torrent) != 2<<20 {
		t.Errorf("the torrent cap must accept a 2 MB body: %d %q", w.Code, w.Body.String())
	}
	r = httptest.NewRequest("POST", "/", strings.NewReader(`{"torrent":`))
	w = httptest.NewRecorder()
	if a.decodeJSON(w, r, &dst) || w.Code != http.StatusBadRequest {
		t.Errorf("malformed JSON: got %d, want 400", w.Code)
	}
}

// A field the handler doesn't take is named in the 400, so a client/server mismatch can
// be read straight off the UI's error line. Malformed JSON keeps the generic message.
func TestDecodeUnknownFieldMessage(t *testing.T) {
	a := &api{}
	var dst struct {
		Name string `json:"name"`
	}
	r := httptest.NewRequest("PUT", "/", strings.NewReader(`{"name":"x","bogus_key":1}`))
	w := httptest.NewRecorder()
	if a.decodeJSON(w, r, &dst) {
		t.Fatal("an unknown field must be rejected")
	}
	var body struct{ Message string }
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %q: %v", w.Body.String(), err)
	}
	if want := `invalid request body: unknown field "bogus_key"`; w.Code != http.StatusBadRequest || body.Message != want {
		t.Errorf("got %d %q, want 400 %q", w.Code, body.Message, want)
	}

	r = httptest.NewRequest("PUT", "/", strings.NewReader(`{"name":`))
	w = httptest.NewRecorder()
	_ = a.decodeJSON(w, r, &dst)
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("decoding %q: %v", w.Body.String(), err)
	}
	if body.Message != "invalid request body" {
		t.Errorf("malformed JSON message = %q, want the generic one", body.Message)
	}
}
