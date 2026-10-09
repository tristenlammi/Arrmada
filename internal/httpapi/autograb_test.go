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

// Specials are a season of their own, so the season grab on them is refused with a reason
// the toast can show — it would otherwise have searched for a pack of the whole show.
func TestAutoGrabRejectsSpecialsSeasonGrab(t *testing.T) {
	a := &api{deps: Deps{Log: slog.New(slog.NewTextHandler(io.Discard, nil))}}
	rec := postAutoGrab(a, `{"season": 0, "episode": 0}`)
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "Specials have no packs") {
		t.Errorf("got %d %s, want 400 naming Specials", rec.Code, rec.Body.String())
	}
}

// No season param is the whole show; season=0 is Specials and must not collapse into it.
func TestHandleSeriesReleasesSeasonSentinel(t *testing.T) {
	cases := []struct {
		season, episode string
		wantS, wantE    int
	}{
		{"", "", -1, 0},
		{"0", "", 0, 0},
		{"0", "5", 0, 5},
		{"3", "", 3, 0},
		{"3", "4", 3, 4},
		{"", "4", -1, 0}, // an episode without a season names nothing
		{"-2", "", -1, 0},
		{"x", "", -1, 0},
	}
	for _, c := range cases {
		s, e := releasesScope(c.season, c.episode)
		if s != c.wantS || e != c.wantE {
			t.Errorf("season=%q episode=%q → (%d, %d), want (%d, %d)", c.season, c.episode, s, e, c.wantS, c.wantE)
		}
	}
}
