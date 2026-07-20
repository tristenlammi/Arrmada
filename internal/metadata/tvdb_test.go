package metadata

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// A minimal TVDB v4 stub: /login issues a token, /episodes/default returns a page.
func tvdbStub(t *testing.T, episodes []tvdbEpisode) *TVDB {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login":
			var body struct {
				APIKey string `json:"apikey"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body.APIKey == "" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"status":"success","data":{"token":"tok-123"}}`)
		case strings.Contains(r.URL.Path, "/episodes/default"):
			if r.Header.Get("Authorization") != "Bearer tok-123" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Query().Get("page") != "0" { // single page in these tests
				_, _ = io.WriteString(w, `{"status":"success","data":{"episodes":[]},"links":{"next":""}}`)
				return
			}
			resp := map[string]any{
				"status": "success",
				"data":   map[string]any{"episodes": episodes},
				"links":  map[string]any{"next": ""},
			}
			_ = json.NewEncoder(w).Encode(resp)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	tv := NewTVDB(func() string { return "a-key" })
	tv.http = srv.Client()
	tv.base = srv.URL
	return tv
}

// The point: TVDB's aired-order seasons plus authoritative absolute numbers, which is what
// anime releases ("Show - 137") are matched against.
func TestTVDBEpisodesWithAbsoluteNumbers(t *testing.T) {
	tv := tvdbStub(t, []tvdbEpisode{
		{SeasonNumber: 1, Number: 1, AbsoluteNumber: 1, Name: "Homecoming", Aired: "2007-02-15", Runtime: 24, Image: "img1"},
		{SeasonNumber: 1, Number: 2, AbsoluteNumber: 2, Name: "The Akatsuki", Aired: "2007-02-15", Runtime: 24},
		{SeasonNumber: 2, Number: 1, AbsoluteNumber: 33, Name: "The Retrieval", Aired: "2007-10-04", Runtime: 24},
		{SeasonNumber: 0, Number: 1, Name: "A Special", Aired: "2008-01-01"},
	})

	seasons, err := tv.Episodes(context.Background(), 79824, "tt0988818")
	if err != nil {
		t.Fatalf("Episodes: %v", err)
	}
	if len(seasons) != 3 {
		t.Fatalf("want specials + 2 seasons, got %d: %+v", len(seasons), seasons)
	}
	if seasons[0].SeasonNumber != 0 {
		t.Errorf("specials must be kept and sorted first, got season %d", seasons[0].SeasonNumber)
	}

	s1 := seasons[1]
	if s1.SeasonNumber != 1 || len(s1.Episodes) != 2 {
		t.Fatalf("season 1 = %+v", s1)
	}
	if s1.Episodes[0].AbsoluteNumber != 1 || s1.Episodes[1].AbsoluteNumber != 2 {
		t.Errorf("absolute numbers not carried: %+v", s1.Episodes)
	}
	// The absolute number is what makes anime work — season 2 episode 1 is absolute 33,
	// NOT 3. A counted number would get this wrong; TVDB's stored one is authoritative.
	if got := seasons[2].Episodes[0].AbsoluteNumber; got != 33 {
		t.Errorf("season 2 episode 1 absolute = %d, want 33 (the whole reason to use TVDB)", got)
	}
	if s1.Episodes[0].Title != "Homecoming" || s1.Episodes[0].StillURL != "img1" {
		t.Errorf("episode fields lost: %+v", s1.Episodes[0])
	}
}

// No key means unavailable — the decorator skips TVDB entirely and falls back.
func TestTVDBUnavailableWithoutKey(t *testing.T) {
	tv := NewTVDB(func() string { return "" })
	if tv.Available() {
		t.Error("no key should mean unavailable")
	}
	seasons, err := tv.Episodes(context.Background(), 79824, "")
	if err != nil || seasons != nil {
		t.Errorf("unavailable source should return (nil, nil), got %+v / %v", seasons, err)
	}
}

// A show with no TVDB id can't be looked up (TVDB is keyed by its own id) — return
// nothing, not an error.
func TestTVDBNoIDIsNotAnError(t *testing.T) {
	tv := tvdbStub(t, nil)
	if seasons, err := tv.Episodes(context.Background(), 0, "tt123"); err != nil || seasons != nil {
		t.Errorf("no tvdb id should be a clean miss, got %+v / %v", seasons, err)
	}
}

// A rejected token must trigger one re-login and retry, not a hard failure — tokens
// expire and we shouldn't surface that to the user.
func TestTVDBReloginsOnExpiredToken(t *testing.T) {
	var logins, rejectsLeft int
	rejectsLeft = 1
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/login":
			logins++
			_, _ = io.WriteString(w, `{"status":"success","data":{"token":"tok-`+itoaN(logins)+`"}}`)
		case strings.Contains(r.URL.Path, "/episodes/default"):
			// Reject the first authed request once, to force a re-login.
			if rejectsLeft > 0 {
				rejectsLeft--
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			_, _ = io.WriteString(w, `{"status":"success","data":{"episodes":[{"seasonNumber":1,"number":1,"absoluteNumber":1,"name":"Ep"}]},"links":{"next":""}}`)
		}
	}))
	t.Cleanup(srv.Close)
	tv := NewTVDB(func() string { return "k" })
	tv.http, tv.base = srv.Client(), srv.URL

	seasons, err := tv.Episodes(context.Background(), 1, "")
	if err != nil {
		t.Fatalf("a re-login should have recovered, got %v", err)
	}
	if len(seasons) != 1 || len(seasons[0].Episodes) != 1 {
		t.Fatalf("expected the retry to succeed, got %+v", seasons)
	}
	if logins != 2 {
		t.Errorf("expected exactly 2 logins (initial + one refresh), got %d", logins)
	}
}

// Source order: TVDB is tried before TVmaze, and the first usable, compatible listing
// wins. This is what makes a configured key take over anime numbering.
func TestSourcesTriedInOrder(t *testing.T) {
	primary := &stubSeries{d: &SeriesDetails{
		SeriesResult: SeriesResult{Title: "Show"}, TVDBID: 1,
		Seasons: []SeasonDetails{{SeasonNumber: 1, Episodes: []EpisodeDetails{{EpisodeNumber: 1, Title: "tmdb"}}}},
	}}
	first := &stubEpisodes{seasons: []SeasonDetails{{SeasonNumber: 1, Episodes: []EpisodeDetails{{EpisodeNumber: 1, Title: "tvdb"}}}}}
	second := &stubEpisodes{seasons: []SeasonDetails{{SeasonNumber: 1, Episodes: []EpisodeDetails{{EpisodeNumber: 1, Title: "tvmaze"}}}}}

	got, _ := NewSeriesWithEpisodes(primary, slog.Default(), first, second).GetSeries(context.Background(), 1)
	if got.Seasons[0].Episodes[0].Title != "tvdb" {
		t.Errorf("first usable source should win, got %q", got.Seasons[0].Episodes[0].Title)
	}
}

// If the first source can't help, the next is tried before giving up.
func TestFallsThroughToLaterSource(t *testing.T) {
	primary := &stubSeries{d: &SeriesDetails{
		SeriesResult: SeriesResult{Title: "Show"}, TVDBID: 1,
		Seasons: []SeasonDetails{{SeasonNumber: 1, Episodes: []EpisodeDetails{{EpisodeNumber: 1, Title: "tmdb"}}}},
	}}
	empty := &stubEpisodes{seasons: nil} // e.g. TVDB doesn't carry it
	good := &stubEpisodes{seasons: []SeasonDetails{{SeasonNumber: 1, Episodes: []EpisodeDetails{{EpisodeNumber: 1, Title: "tvmaze"}}}}}

	got, _ := NewSeriesWithEpisodes(primary, slog.Default(), empty, good).GetSeries(context.Background(), 1)
	if got.Seasons[0].Episodes[0].Title != "tvmaze" {
		t.Errorf("should fall through to the second source, got %q", got.Seasons[0].Episodes[0].Title)
	}
}

// An unavailable source (no key) is dropped, so only TVmaze wraps.
func TestUnavailableSourcesAreDropped(t *testing.T) {
	primary := &stubSeries{d: &SeriesDetails{SeriesResult: SeriesResult{Title: "Show"}}}
	unavailable := &stubUnavailable{}
	if p := NewSeriesWithEpisodes(primary, slog.Default(), unavailable); p != SeriesProvider(primary) {
		t.Error("with every source unavailable, the primary should be returned unwrapped")
	}
}

type stubUnavailable struct{}

func (stubUnavailable) Available() bool { return false }
func (stubUnavailable) Episodes(context.Context, int, string) ([]SeasonDetails, error) {
	return nil, nil
}

func itoaN(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
