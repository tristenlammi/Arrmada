package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/insights"
	"github.com/tristenlammi/arrmada/internal/requests"
)

// fakeLinker knows a fixed set of titles by TMDB id ("movie:603") or IMDb id
// ("movie:tt0133093") and records what it was asked.
type fakeLinker struct {
	mu       sync.Mutex
	known    map[string]string
	asked    []insights.ExternalIDs
	notReady bool // Plex not set up, or the index not built yet
}

func (f *fakeLinker) PlexIndexReady(context.Context) bool { return !f.notReady }

func (f *fakeLinker) WatchURL(_ context.Context, media string, ids insights.ExternalIDs) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, ids)
	if u := f.known[media+":"+strconv.Itoa(ids.TMDB)]; u != "" {
		return u
	}
	if ids.IMDB != "" {
		return f.known[media+":"+ids.IMDB]
	}
	return ""
}

func TestPlexLinkRoute(t *testing.T) {
	link := &fakeLinker{known: map[string]string{"movie:603": "https://app.plex.tv/desktop/#!/server/m/details?key=%2Flibrary%2Fmetadata%2F1"}}
	s := newRouteServer(t, func(d *Deps) { d.PlexLinks = link })
	_, kid := s.user(t, "kid@example.com", auth.RoleRequester)

	rec := s.do("GET", "/api/v1/plex/link?media_type=movie&tmdb_id=603", kid)
	if rec.Code != http.StatusOK {
		t.Fatalf("known title: %d %s", rec.Code, rec.Body)
	}
	var body struct{ URL string }
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.URL != link.known["movie:603"] {
		t.Fatalf("body = %s (%v)", rec.Body, err)
	}
	if rec := s.do("GET", "/api/v1/plex/link?media_type=series&tmdb_id=603", kid); rec.Code != http.StatusNoContent {
		t.Errorf("unknown title: %d, want 204", rec.Code)
	}
	if rec := s.do("GET", "/api/v1/plex/link?media_type=book&tmdb_id=1", kid); rec.Code != http.StatusBadRequest {
		t.Errorf("bad media type: %d", rec.Code)
	}
	if rec := s.do("GET", "/api/v1/plex/link?media_type=movie&tmdb_id=603", nil); rec.Code != http.StatusUnauthorized {
		t.Errorf("signed out: %d", rec.Code)
	}

	// Without a linker (Plex not wired) every title is simply not in Plex.
	bare := newRouteServer(t, nil)
	_, kid2 := bare.user(t, "kid2@example.com", auth.RoleRequester)
	if rec := bare.do("GET", "/api/v1/plex/link?media_type=movie&tmdb_id=603", kid2); rec.Code != http.StatusNoContent {
		t.Errorf("no linker: %d, want 204", rec.Code)
	}
}

func mediaDetailJSON(t *testing.T, a *api) map[string]any {
	t.Helper()
	// The response carries the title's card too; give it an empty library to read
	// instead of real services.
	if _, ok := enrichSnaps.Load(a); !ok {
		enrichSnaps.Store(a, &enrichSnapEntry{at: time.Now().Add(time.Hour), snap: emptySnap()})
		t.Cleanup(func() { enrichSnaps.Delete(a) })
	}
	r := httptest.NewRequest("GET", "/api/v1/media/movie/603", nil)
	r.SetPathValue("media", "movie")
	r.SetPathValue("id", "603")
	rec := httptest.NewRecorder()
	a.handleMediaDetail(rec, r)
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || rec.Code != http.StatusOK {
		t.Fatalf("HTTP %d %s", rec.Code, rec.Body)
	}
	return body
}

func TestMediaDetailIncludesPlexURLWhenLinked(t *testing.T) {
	// A legacy-agent library knows the film only by IMDb: the detail's own IMDb id finds it.
	link := &fakeLinker{known: map[string]string{"movie:tt0133093": "https://app.plex.tv/desktop/#!/server/m/details?key=%2Flibrary%2Fmetadata%2F9"}}
	a := &api{deps: Deps{Discovery: detailStub{stubDiscovery{ok: true}}, PlexLinks: link}}
	body := mediaDetailJSON(t, a)
	if body["plex_url"] != link.known["movie:tt0133093"] || body["title"] != "The Matrix" || body["imdb_id"] != "tt0133093" {
		t.Fatalf("detail = %v", body)
	}
}

func TestMediaDetailOmitsWhenUnconfigured(t *testing.T) {
	a := &api{deps: Deps{Discovery: detailStub{stubDiscovery{ok: true}}}}
	if body := mediaDetailJSON(t, a); body["plex_url"] != nil || body["title"] != "The Matrix" {
		t.Fatalf("detail without Plex = %v", body)
	}
	a.deps.PlexLinks = &fakeLinker{known: map[string]string{}}
	if body := mediaDetailJSON(t, a); body["plex_url"] != nil {
		t.Fatalf("detail for a title Plex doesn't have = %v", body)
	}
}

func TestListRequestsPlexURLOnlyWhenAvailable(t *testing.T) {
	link := &fakeLinker{known: map[string]string{"movie:1": "u1", "movie:2": "u2", "series:3": "u3"}}
	a := &api{deps: Deps{PlexLinks: link}}
	list := []requests.Request{
		{MediaType: "movie", TMDBID: 1, Tracking: &requests.Tracking{Stage: requests.StageAvailable}},
		{MediaType: "movie", TMDBID: 2, Tracking: &requests.Tracking{Stage: requests.StageDownloading}},
		{MediaType: "series", TMDBID: 3, Tracking: &requests.Tracking{Stage: requests.StagePartial}},
		{MediaType: "movie", TMDBID: 4, Tracking: &requests.Tracking{Stage: requests.StageAvailable}}, // not in Plex yet
		{MediaType: "book", TMDBID: 1, Tracking: &requests.Tracking{Stage: requests.StageAvailable}},
		{MediaType: "movie", TMDBID: 1}, // never tracked
	}
	a.setRequestPlexURLs(context.Background(), list)
	got := []string{}
	for _, rq := range list {
		got = append(got, rq.PlexURL)
	}
	if want := []string{"u1", "", "u3", "", "", ""}; strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("plex urls = %q, want %q", got, want)
	}

	// While the index isn't ready nothing is looked up at all.
	idle := &fakeLinker{known: link.known, notReady: true}
	a.deps.PlexLinks = idle
	list[0].PlexURL = ""
	a.setRequestPlexURLs(context.Background(), list)
	if list[0].PlexURL != "" || len(idle.asked) != 0 {
		t.Fatalf("not ready: url %q after %d lookups", list[0].PlexURL, len(idle.asked))
	}
}

// A 'ready' notice in the inbox carries the title's Watch on Plex page — Plex's current
// one when the index has the title, else the one saved with it — and nothing else does.
// While Plex isn't set up or its index isn't built, every link is hidden.
func TestInboxPlexLinks(t *testing.T) {
	link := &fakeLinker{known: map[string]string{"movie:603": "live603", "series:1399": "live1399"}}
	a := &api{deps: Deps{PlexLinks: link}}
	items := func() []requests.UserNotification {
		return []requests.UserNotification{
			{Ref: "movie:603"},                      // ready, Plex has it now
			{Ref: "movie:604", PlexURL: "saved604"}, // ready, saved link (legacy-agent match)
			{Ref: "series:1399:r12:s2"},             // a season's ready notice
			{Ref: "movie:603:approved:1700000000"},  // a decision: no link
			{Ref: "book:OL1W"},                      // books aren't in Plex
			{Ref: "request:40"},                     // staff 'new request'
		}
	}
	got := items()
	a.inboxPlexLinks(context.Background(), got)
	want := []string{"live603", "saved604", "live1399", "", "", ""}
	for i, n := range got {
		if n.PlexURL != want[i] {
			t.Errorf("%s: plex_url %q, want %q", n.Ref, n.PlexURL, want[i])
		}
	}
	link.notReady = true
	got = items()
	a.inboxPlexLinks(context.Background(), got)
	for _, n := range got {
		if n.PlexURL != "" {
			t.Errorf("Plex not ready: %s still links %q", n.Ref, n.PlexURL)
		}
	}
}
