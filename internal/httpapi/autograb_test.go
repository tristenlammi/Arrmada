package httpapi

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func postAutoGrab(a *api, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/v1/series/7/autograb", strings.NewReader(body))
	req.SetPathValue("id", "7")
	rec := httptest.NewRecorder()
	a.handleAutoGrabSeries(rec, req)
	return rec
}

// A scope no button sends is refused up front, before any background search starts —
// there's no automation wired here, so reaching the search would panic the test.
func TestAutoGrabRejectsBadScopes(t *testing.T) {
	a := &api{deps: Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	for _, body := range []string{
		`{"season": -1, "episode": 0}`,
		`{"season": 2, "episode": -3}`,
		`{"season": 0, "episode": 0}`,
	} {
		if rec := postAutoGrab(a, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s, want 400", body, rec.Code, rec.Body.String())
		}
	}
}
