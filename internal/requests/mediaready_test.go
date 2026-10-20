package requests

import (
	"context"
	"fmt"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/mediaserver"
	"github.com/tristenlammi/arrmada/internal/movies"
)

// serverIndex is one fake media server's index: titles by "movie:603" / "series:77" /
// "folder:/movies/Dune (2021)".
type serverIndex struct {
	kind  mediaserver.Kind
	built time.Time
	have  map[string]bool
}

// twoServers is a PlexLocator over Plex and Jellyfin, the way main's Registry adapter
// answers: a sighting per server that has the title, oldest index, refresh both.
type twoServers struct {
	mu        sync.Mutex
	servers   []*serverIndex
	refreshes int
}

func newTwoServers() *twoServers {
	return &twoServers{servers: []*serverIndex{
		{kind: mediaserver.Plex, have: map[string]bool{}},
		{kind: mediaserver.Jellyfin, have: map[string]bool{}},
	}}
}

func (f *twoServers) get(k mediaserver.Kind) *serverIndex {
	for _, s := range f.servers {
		if s.kind == k {
			return s
		}
	}
	return nil
}

// rebuilt is server k's index read at t, with these titles added.
func (f *twoServers) rebuilt(k mediaserver.Kind, t time.Time, keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := f.get(k)
	s.built = t
	for _, key := range keys {
		s.have[key] = true
	}
}

func (f *twoServers) Configured(context.Context) bool { return true }

func (f *twoServers) Servers(context.Context) []mediaserver.Kind {
	return []mediaserver.Kind{mediaserver.Plex, mediaserver.Jellyfin}
}

func (f *twoServers) Find(_ context.Context, media string, ids PlexIDs) []Sighting {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []Sighting
	for _, s := range f.servers {
		if s.built.IsZero() {
			continue
		}
		keys := []string{fmt.Sprintf("%s:%d", media, ids.TMDB)}
		if ids.Folder != "" && s.kind == mediaserver.Jellyfin {
			keys = append(keys, "folder:"+ids.Folder) // only Jellyfin finds by folder
		}
		for _, k := range keys {
			if s.have[k] {
				out = append(out, Sighting{Link: mediaserver.WatchLink{Server: s.kind, Label: s.kind.Label(), URL: watchURLFor(s.kind, media, ids.TMDB)}, IndexBuiltAt: s.built})
				break
			}
		}
	}
	return out
}

func watchURLFor(k mediaserver.Kind, media string, tmdb int) string {
	if k == mediaserver.Jellyfin {
		return fmt.Sprintf("https://jf.example.com/web/#/details?id=%s%d&serverId=abc", media, tmdb)
	}
	return fmt.Sprintf("https://app.plex.tv/desktop/#!/server/m1/details?key=%s%d", media, tmdb)
}

func (f *twoServers) IndexBuiltAt() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	var oldest time.Time
	for i, s := range f.servers {
		if s.built.IsZero() {
			return time.Time{}
		}
		if i == 0 || s.built.Before(oldest) {
			oldest = s.built
		}
	}
	return oldest
}

func (f *twoServers) Refresh(time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refreshes++
}

// twoServerFixture is plexMovieFixture with both servers set up, user 7 asking.
func twoServerFixture(t *testing.T) (*Service, *twoServers, *testClock, *urlPush, Request) {
	t.Helper()
	s, _, _ := quietFixture(t, movies.Movie{TMDBID: 603, Title: "Dune", HasFile: true, IMDBID: "tt1160419", MovieFilePath: "/movies/Dune (2021)/Dune (2021) - 1080p.mkv"})
	s.coord = nil
	loc := newTwoServers()
	clk := &testClock{t: time.Now()}
	s.now = clk.now
	s.SetPlexLocator(loc)
	pushes := &urlPush{}
	s.push = pushes
	req, err := s.repo.Create(context.Background(), Request{MediaType: "movie", TMDBID: 603, Title: "Dune", Status: StatusApproved, RequestedBy: 7})
	if err != nil {
		t.Fatal(err)
	}
	return s, loc, clk, pushes, req
}

// Owner decision: ready as soon as EITHER server has it. Plex is set up and read but
// hasn't got the film; Jellyfin has: the requester is told at once, "on Jellyfin", with
// Jellyfin's link on the inbox row and as the push's Watch action — and no Plex link.
func TestReadyWhenOnlyJellyfinHasIt(t *testing.T) {
	s, loc, clk, pushes, req := twoServerFixture(t)
	ctx := context.Background()
	loc.rebuilt(mediaserver.Plex, clk.now())
	loc.rebuilt(mediaserver.Jellyfin, clk.now(), "movie:603")
	if err := s.NotifyMovieReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	inbox, err := s.repo.listUserNotifications(ctx, 7)
	if err != nil || len(inbox) != 1 {
		t.Fatalf("inbox = %+v (%v), want one notice", inbox, err)
	}
	jf := watchURLFor(mediaserver.Jellyfin, "movie", 603)
	if inbox[0].Body != "“Dune” is ready to watch on Jellyfin." || inbox[0].JellyfinURL != jf || inbox[0].PlexURL != "" {
		t.Errorf("notice = %+v", inbox[0])
	}
	pushes.mu.Lock()
	watch, plex := append([]string(nil), pushes.watch...), append([]string(nil), pushes.plex...)
	pushes.mu.Unlock()
	if !reflect.DeepEqual(watch, []string{"Jellyfin " + jf}) || !reflect.DeepEqual(plex, []string{""}) {
		t.Errorf("push watch %v plex %v", watch, plex)
	}
	if r, _ := s.repo.Get(ctx, req.ID); r.ReadyAt == 0 {
		t.Error("ready_at not stamped")
	}
}

// Both servers have it: the notice names both and keeps both links; each person's push and
// Apprise lead with their own server — Jellyfin for someone who watches there.
func TestReadyOnBothLeadsWithTheirServer(t *testing.T) {
	s, loc, clk, pushes, _ := twoServerFixture(t)
	ctx := context.Background()
	s.SetHomeServer(func(_ context.Context, uid int64) mediaserver.Kind {
		if uid == 7 {
			return mediaserver.Jellyfin
		}
		return mediaserver.Plex
	})
	var apprise []string
	s.userApprise.resolver = fixedResolver{"push.example.com": "93.184.216.34"}
	s.userApprise.send = func(_ context.Context, _, _, body, _ string) error { apprise = append(apprise, body); return nil }
	if _, err := s.repo.db.ExecContext(ctx, `INSERT INTO users (id, username, role, password_hash, apprise_url) VALUES (7, 'kid', 'requester', 'x', 'gotify://push.example.com/token')`); err != nil {
		t.Fatal(err)
	}
	loc.rebuilt(mediaserver.Plex, clk.now(), "movie:603")
	loc.rebuilt(mediaserver.Jellyfin, clk.now(), "movie:603")
	if err := s.NotifyMovieReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	plex, jf := watchURLFor(mediaserver.Plex, "movie", 603), watchURLFor(mediaserver.Jellyfin, "movie", 603)
	inbox, _ := s.repo.listUserNotifications(ctx, 7)
	if len(inbox) != 1 || inbox[0].Body != "“Dune” is ready to watch on Plex and Jellyfin." || inbox[0].PlexURL != plex || inbox[0].JellyfinURL != jf {
		t.Fatalf("inbox = %+v", inbox)
	}
	pushes.mu.Lock()
	watch, plexPush := append([]string(nil), pushes.watch...), append([]string(nil), pushes.plex...)
	pushes.mu.Unlock()
	if !reflect.DeepEqual(watch, []string{"Jellyfin " + jf}) || !reflect.DeepEqual(plexPush, []string{plex}) {
		t.Errorf("push watch %v plex %v, want Jellyfin's Watch and Plex's link kept", watch, plexPush)
	}
	want := "“Dune” is ready to watch on Plex and Jellyfin.\nWatch on Jellyfin: " + jf + "\nWatch on Plex: " + plex
	if len(apprise) != 1 || apprise[0] != want {
		t.Errorf("apprise = %q, want %q", apprise, want)
	}
}

// Jellyfin lists each file of an unmatched multi-version folder as its own movie with no
// ids: the film's folder from the library record finds it.
func TestReadyFindsJellyfinByFolder(t *testing.T) {
	s, loc, clk, _, _ := twoServerFixture(t)
	ctx := context.Background()
	loc.rebuilt(mediaserver.Plex, clk.now())
	loc.rebuilt(mediaserver.Jellyfin, clk.now(), "folder:/movies/Dune (2021)")
	if err := s.NotifyMovieReady(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if got := plexInbox(t, s, 7); len(got) != 1 || got[0] != "“Dune” is ready to watch on Jellyfin." {
		t.Fatalf("inbox = %v, want found by its folder", got)
	}
}

// Neither shows it within the grace period: the notice names both servers.
func TestReadyLateNamesBothServers(t *testing.T) {
	s, loc, clk, _, _ := twoServerFixture(t)
	ctx := context.Background()
	loc.rebuilt(mediaserver.Plex, clk.now())
	loc.rebuilt(mediaserver.Jellyfin, clk.now())
	_ = s.NotifyMovieReady(ctx, 1)
	if got := plexInbox(t, s, 7); len(got) != 0 {
		t.Fatalf("told before any server had it: %v", got)
	}
	clk.add(31 * time.Minute)
	if err := s.CheckPlexWaiting(ctx); err != nil {
		t.Fatal(err)
	}
	want := "“Dune” is ready — it may take a few more minutes to show up in Plex and Jellyfin."
	if got := plexInbox(t, s, 7); len(got) != 1 || got[0] != want {
		t.Fatalf("inbox = %v, want %q", got, want)
	}
}

// A show both servers had from an earlier season: Plex's index is old (Plex is down and
// can't be read again), Jellyfin's was read after the season landed. Jellyfin's answer
// counts on its own, so the requester isn't held for the grace period by a Plex that's
// down — and the notice names only the server whose answer counted.
func TestShowReadyOnTheServerWithAFreshIndex(t *testing.T) {
	f := newApproveFixture(t, showListing(2, 2))
	loc := newTwoServers()
	clk := &testClock{t: time.Now()}
	f.s.now = clk.now
	f.s.SetPlexLocator(loc)
	loc.rebuilt(mediaserver.Plex, clk.now().Add(-time.Hour), "series:77")
	loc.rebuilt(mediaserver.Jellyfin, clk.now().Add(-time.Hour), "series:77")
	sr, err := f.shows().Add(f.ctx, 77, "", false)
	if err != nil {
		t.Fatal(err)
	}
	fillSeason(t, f, sr.ID, 1, 2)
	req, _, err := f.s.Create(f.ctx, Request{MediaType: "series", TMDBID: 77, Title: "Show", Seasons: []int{2}, KnownSeasons: []int{1, 2}, RequestedBy: 7}, CreateOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.s.Approve(f.ctx, req.ID, ApproveOptions{}); err != nil {
		t.Fatal(err)
	}
	fillSeason(t, f, sr.ID, 2, 2)
	_ = f.s.NotifySeriesReady(f.ctx, sr.ID)
	readyRef := fmt.Sprintf("series:77:r%d", req.ID)
	if contains(refs(inbox(t, f, 7)), readyRef) {
		t.Fatal("told on indexes read before the season landed")
	}
	clk.add(3 * time.Minute)
	loc.rebuilt(mediaserver.Jellyfin, clk.now())
	if err := f.s.CheckPlexWaiting(f.ctx); err != nil {
		t.Fatal(err)
	}
	var body string
	for _, n := range inbox(t, f, 7) {
		if n.Ref == readyRef {
			body = n.Body
		}
	}
	if body != "Season 2 of “Show” is ready to watch on Jellyfin." {
		t.Fatalf("ready body = %q", body)
	}
}

// The card names what it's being added to: one server, or both.
func TestTrackAddingNamesTheServers(t *testing.T) {
	s, _, _, _, req := twoServerFixture(t)
	ctx := context.Background()
	_ = s.NotifyMovieReady(ctx, 1)
	list, _, err := s.List(ctx, ListFilter{})
	if err != nil {
		t.Fatal(err)
	}
	s.Track(ctx, list, nil, true)
	for _, r := range list {
		if r.ID != req.ID {
			continue
		}
		if r.Tracking == nil || r.Tracking.Stage != StageAdding || !reflect.DeepEqual(r.Tracking.AddingTo, []mediaserver.Kind{mediaserver.Plex, mediaserver.Jellyfin}) {
			t.Fatalf("tracking = %+v, want adding to Plex and Jellyfin", r.Tracking)
		}
	}
}
