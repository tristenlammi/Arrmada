package httpapi

import (
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
