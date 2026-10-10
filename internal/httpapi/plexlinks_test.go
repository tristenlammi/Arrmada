package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"sync"
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/insights"
)

// fakeLinker knows a fixed set of titles ("movie:603") and records what it was asked.
type fakeLinker struct {
	mu    sync.Mutex
	known map[string]string
	asked []insights.ExternalIDs
}

func (f *fakeLinker) WatchURL(_ context.Context, media string, ids insights.ExternalIDs) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, ids)
	key := media + ":" + strconv.Itoa(ids.TMDB)
	return f.known[key]
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
