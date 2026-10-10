package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tristenlammi/arrmada/internal/audioserver"
	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/listening"
	"github.com/tristenlammi/arrmada/internal/metadata"
)

// audioHarness is the real router with the audiobook server behind it: one book with a
// three-file audiobook (fake mp3s in a temp dir), the server switched on, and every log
// line captured so a test can check nothing names what anyone listens to.
type audioHarness struct {
	t      *testing.T
	rs     *routeServer
	srv    *audioserver.Server
	key    string
	bookID int64
	title  string
	data   string // the audiobook server's data dir (covers live in data/covers)
	logs   *syncBuffer
}

// syncBuffer is a log sink handlers write to while the test reads it.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func newAudioHarness(t *testing.T) *audioHarness {
	t.Helper()
	h := &audioHarness{t: t, logs: &syncBuffer{}, title: "Dungeon Crawler Carl"}
	log := slog.New(slog.NewTextHandler(h.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	h.rs = newRouteServer(t, func(d *Deps) {
		d.Log = log
		ctx := context.Background()
		db := d.Store.DB()
		bs := books.NewService(db, nil, log)
		added, _ := bs.AddWorks(ctx, []metadata.BookResult{{Key: "hc:1", Title: h.title, Author: "Matt Dinniman"}}, "", true)
		if len(added) != 1 {
			t.Fatal("book not added")
		}
		dir := filepath.Join(t.TempDir(), "Matt Dinniman", h.title)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, n := range []string{"Part 1.mp3", "Part 2.mp3"} {
			if err := os.WriteFile(filepath.Join(dir, n), bytes.Repeat([]byte(n[:6]), 4000), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := bs.MarkImported(ctx, added[0].ID, books.KindAudiobook, dir, "MP3", 48000, 2); err != nil {
			t.Fatal(err)
		}
		h.bookID = added[0].ID
		h.key = audioserver.ItemKey(added[0].ID, 0)
		h.data = t.TempDir()
		h.srv = audioserver.New(audioserver.Options{DB: db, Books: bs, Listen: listening.NewStore(db), Users: d.Auth,
			Settings: d.Settings, Log: log, FFprobe: "ffprobe-not-installed", DataDir: h.data})
		// An uploaded cover, as Books' cover upload stores it.
		if err := os.MkdirAll(filepath.Join(h.data, "covers"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(h.data, "covers", fmt.Sprintf("book-%d.png", h.bookID)), []byte("fake png"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := bs.SetCover(ctx, h.bookID, fmt.Sprintf("/api/v1/books/%d/cover-image?v=1", h.bookID)); err != nil {
			t.Fatal(err)
		}
		d.AudioServer = h.srv
		if err := d.Settings.Set(ctx, audioserver.KeyEnabled, "true"); err != nil {
			t.Fatal(err)
		}
	})
	return h
}

// req sends a request as the cookie's account. external makes it come from the internet
// (through the tunnel) rather than the LAN.
func (h *audioHarness) req(method, path string, body any, c *http.Cookie, external bool, hdr ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		if raw, ok := body.(string); ok {
			rd = bytes.NewReader([]byte(raw))
		} else {
			b, _ := json.Marshal(body)
			rd = bytes.NewReader(b)
		}
	}
	r := httptest.NewRequest(method, "http://arrmada.local"+path, rd)
	r.RemoteAddr = "192.168.1.20:5000"
	if external {
		r.RemoteAddr = "8.8.8.8:5000"
	}
	if body != nil {
		r.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(hdr); i += 2 {
		r.Header.Set(hdr[i], hdr[i+1])
	}
	if c != nil {
		r.AddCookie(c)
	}
	rec := httptest.NewRecorder()
	h.rs.h.ServeHTTP(rec, r)
	return rec
}

// ok sends a request that must answer 200 and decodes the JSON reply.
func (h *audioHarness) ok(method, path string, body any, c *http.Cookie) map[string]any {
	h.t.Helper()
	rec := h.req(method, path, body, c, false)
	if rec.Code != http.StatusOK {
		h.t.Fatalf("%s %s: HTTP %d %s", method, path, rec.Code, rec.Body)
	}
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		h.t.Fatalf("%s %s: not a JSON object: %s", method, path, rec.Body)
	}
	return m
}

// The You page gets a "later spot" offer for an upload that wasn't used and a Recently
// removed list, and the undiscard and dismiss actions only ever touch the caller's own.
func TestMyAudioOffersAndRemoved(t *testing.T) {
	h := newAudioHarness(t)
	kid, kidC := h.rs.user(t, "kid", auth.RoleRequester)
	_, momC := h.rs.user(t, "mom", auth.RoleRequester)
	ctx := context.Background()
	store := h.srv.Listen()

	// The kid's place, then a stale upload of a later spot (an offline phone).
	if _, err := store.SetProgress(ctx, kid.ID, h.key, 3600, 36000, nil, "web"); err != nil {
		t.Fatal(err)
	}
	stale := time.Now().Add(-time.Hour).UnixMilli()
	if _, err := store.ReportPosition(ctx, kid.ID, h.key, 14531, 36000, false, stale, "Pixel"); err != nil {
		t.Fatal(err)
	}
	me := h.ok("GET", "/api/v1/me/audio", nil, kidC)
	places := me["places"].([]any)
	if len(places) != 1 {
		t.Fatalf("places = %v", places)
	}
	offer, _ := places[0].(map[string]any)["offer"].(map[string]any)
	if offer == nil || offer["position"] != 14531.0 || offer["device"] != "Pixel" {
		t.Fatalf("offer = %v, want the 14531 from the Pixel", places[0])
	}
	hid := int64(offer["history_id"].(float64))

	// Someone else can't dismiss (or see) it.
	if rec := h.req("POST", "/api/v1/me/audio/dismiss", map[string]any{"item": h.key, "history_id": hid}, momC, false); rec.Code != http.StatusNotFound {
		t.Fatalf("another account dismissing it: HTTP %d, want 404", rec.Code)
	}
	if mom := h.ok("GET", "/api/v1/me/audio", nil, momC); len(mom["places"].([]any)) != 0 || len(mom["removed"].([]any)) != 0 {
		t.Fatalf("another account sees the kid's places: %v", mom)
	}
	if rec := h.req("POST", "/api/v1/me/audio/dismiss", map[string]any{"item": h.key, "history_id": hid}, kidC, false); rec.Code != http.StatusNoContent {
		t.Fatalf("dismiss: HTTP %d %s", rec.Code, rec.Body)
	}
	if o := h.ok("GET", "/api/v1/me/audio", nil, kidC)["places"].([]any)[0].(map[string]any)["offer"]; o != nil {
		t.Fatalf("a dismissed offer is still offered: %v", o)
	}

	// An app removes the place: it moves to removed, and comes back exactly.
	if err := store.DeleteProgress(ctx, kid.ID, h.key, "Lissen"); err != nil {
		t.Fatal(err)
	}
	me = h.ok("GET", "/api/v1/me/audio", nil, kidC)
	removed := me["removed"].([]any)
	if len(me["places"].([]any)) != 0 || len(removed) != 1 {
		t.Fatalf("after removal: places %v removed %v", me["places"], removed)
	}
	rm := removed[0].(map[string]any)
	if rm["position"] != 3600.0 || rm["device"] != "Lissen" || rm["title"] != h.title || rm["discarded_at"].(float64) <= 0 {
		t.Fatalf("removed row = %v", rm)
	}
	if rec := h.req("POST", "/api/v1/me/audio/undiscard", map[string]any{"item": h.key}, momC, false); rec.Code != http.StatusBadRequest {
		t.Fatalf("another account undiscarding it: HTTP %d, want 400", rec.Code)
	}
	if r := h.ok("POST", "/api/v1/me/audio/undiscard", map[string]any{"item": h.key}, kidC); r["position"] != 3600.0 {
		t.Fatalf("undiscard = %v", r)
	}
	me = h.ok("GET", "/api/v1/me/audio", nil, kidC)
	if len(me["places"].([]any)) != 1 || len(me["removed"].([]any)) != 0 {
		t.Fatalf("after undiscard: %v", me)
	}
	// The undiscard and dismiss routes work through the tunnel for a requester.
	if rec := h.req("POST", "/api/v1/me/audio/undiscard", map[string]any{"item": h.key}, kidC, true); rec.Code != http.StatusBadRequest {
		t.Fatalf("undiscard from outside: HTTP %d %s, want it to reach the handler (400: nothing to restore)", rec.Code, rec.Body)
	}
}

// decode reads a JSON object reply.
func decode(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("not a JSON object: %s", rec.Body)
	}
	return m
}

// An allowed requester can browse the shelves, the library and a book, and fetch its
// cover — from home and through the tunnel.
func TestWebPlayerShelvesAndDetail(t *testing.T) {
	h := newAudioHarness(t)
	kid, kidC := h.rs.user(t, "kid", auth.RoleRequester)
	ctx := context.Background()
	if _, err := h.srv.Listen().SetProgress(ctx, kid.ID, h.key, 1200, 0, nil, "web"); err != nil {
		t.Fatal(err)
	}
	for _, external := range []bool{false, true} {
		rec := h.req("GET", "/api/v1/me/audio/shelves", nil, kidC, external)
		if rec.Code != http.StatusOK {
			t.Fatalf("shelves (external %v): HTTP %d %s", external, rec.Code, rec.Body)
		}
		shelves := decode(t, rec)["shelves"].([]any)
		first := shelves[0].(map[string]any)
		if first["id"] != "continue-listening" || first["label"] != "Continue Listening" {
			t.Fatalf("first shelf = %v", first)
		}
		card := first["items"].([]any)[0].(map[string]any)
		if card["key"] != h.key || card["title"] != h.title || card["author"] != "Matt Dinniman" ||
			card["progress"].(map[string]any)["position"] != 1200.0 {
			t.Fatalf("card = %v", card)
		}
		cover, _ := card["cover"].(string)
		if !strings.HasPrefix(cover, "/api/v1/me/audio/items/"+h.key+"/cover?v=") {
			t.Fatalf("cover = %q", cover)
		}
		rec = h.req("GET", cover, nil, kidC, external)
		if rec.Code != http.StatusOK || !strings.HasPrefix(rec.Header().Get("Cache-Control"), "private") {
			t.Fatalf("cover (external %v): HTTP %d, Cache-Control %q", external, rec.Code, rec.Header().Get("Cache-Control"))
		}
	}

	lib := h.ok("GET", "/api/v1/me/audio/library?q=crawler&sort=added&page=0&limit=10", nil, kidC)
	if lib["total"] != 1.0 || len(lib["items"].([]any)) != 1 || lib["limit"] != 10.0 {
		t.Fatalf("library search = %v", lib)
	}
	if lib := h.ok("GET", "/api/v1/me/audio/library?q=nothing-like-it", nil, kidC); lib["total"] != 0.0 || len(lib["items"].([]any)) != 0 {
		t.Fatalf("library search for nothing = %v", lib)
	}
	if lib := h.ok("GET", "/api/v1/me/audio/library?filter=not-started", nil, kidC); lib["total"] != 0.0 {
		t.Fatalf("not-started filter = %v, want the started book left out", lib)
	}
	if lib := h.ok("GET", "/api/v1/me/audio/library?filter=in-progress", nil, kidC); lib["total"] != 1.0 {
		t.Fatalf("in-progress filter = %v", lib)
	}
	if lib := h.ok("GET", "/api/v1/me/audio/library?page=1", nil, kidC); lib["total"] != 1.0 || len(lib["items"].([]any)) != 0 {
		t.Fatalf("page past the end = %v", lib)
	}

	if _, err := h.srv.Listen().AddBookmark(ctx, kid.ID, h.key, 300, "Good bit"); err != nil {
		t.Fatal(err)
	}
	d := h.ok("GET", "/api/v1/me/audio/items/"+h.key, nil, kidC)
	tracks := d["tracks"].([]any)
	if d["key"] != h.key || len(tracks) != 2 || d["chapters"] == nil || d["versions"] == nil {
		t.Fatalf("detail = %v", d)
	}
	tr := tracks[1].(map[string]any)
	if tr["index"] != 2.0 || tr["mime"] != "audio/mpeg" || !strings.HasPrefix(tr["url"].(string), "/api/v1/me/audio/items/"+h.key+"/file/") {
		t.Fatalf("track = %v", tr)
	}
	if bm := d["bookmarks"].([]any); len(bm) != 1 || bm[0].(map[string]any)["title"] != "Good bit" {
		t.Fatalf("bookmarks = %v", bm)
	}
	if rec := h.req("GET", "/api/v1/me/audio/items/b999", nil, kidC, false); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown item: HTTP %d", rec.Code)
	}
}

// The listening API follows the audiobook server's one switch and its allow-list.
func TestWebPlayerRespectsAllowListAndSwitch(t *testing.T) {
	h := newAudioHarness(t)
	kid, kidC := h.rs.user(t, "kid", auth.RoleRequester)
	_, viewC := h.rs.user(t, "viewer", auth.RoleReadonly)
	ctx := context.Background()
	paths := []string{"/api/v1/me/audio/shelves", "/api/v1/me/audio/library", "/api/v1/me/audio/items/" + h.key,
		"/api/v1/me/audio/items/" + h.key + "/cover"}
	expect := func(c *http.Cookie, code int, msg string) {
		t.Helper()
		for _, p := range paths {
			rec := h.req("GET", p, nil, c, false)
			if rec.Code != code {
				t.Errorf("GET %s: HTTP %d, want %d", p, rec.Code, code)
				continue
			}
			if msg != "" && decode(t, rec)["message"] != msg {
				t.Errorf("GET %s: %s, want %q", p, rec.Body, msg)
			}
		}
	}
	expect(nil, http.StatusUnauthorized, "")
	expect(viewC, http.StatusForbidden, "Your account isn't set up for audiobooks")
	if err := h.srv.SetAllowed(ctx, kid.ID, false); err != nil {
		t.Fatal(err)
	}
	expect(kidC, http.StatusForbidden, "Your account isn't set up for audiobooks")
	if err := h.srv.SetAllowed(ctx, kid.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := h.rs.deps.Settings.Set(ctx, audioserver.KeyEnabled, "false"); err != nil {
		t.Fatal(err)
	}
	expect(kidC, http.StatusForbidden, "Audiobooks are switched off")
}

// noItemInLogs fails when any captured log line names the item, its book, its title or
// its author, or pairs the account with an item route — admins see how much and when
// people listen, never what.
func (h *audioHarness) noItemInLogs(user string, wantRoutes ...string) {
	h.t.Helper()
	logged := h.logs.String()
	keyRe := regexp.MustCompile(`(^|[^a-z0-9])` + regexp.QuoteMeta(h.key) + `([^0-9v]|$)`)
	if keyRe.MatchString(logged) {
		h.t.Errorf("the log names item %s:\n%s", h.key, logged)
	}
	for _, secret := range []string{h.title, "Dungeon", "Dinniman", "Good bit", "crawler"} {
		if strings.Contains(logged, secret) {
			h.t.Errorf("the log contains %q:\n%s", secret, logged)
		}
	}
	for _, line := range strings.Split(logged, "\n") {
		if strings.Contains(line, user) && strings.Contains(line, "/me/audio/") {
			h.t.Errorf("a line pairs the user with a listening route: %s", line)
		}
	}
	for _, want := range wantRoutes {
		if !strings.Contains(logged, want) {
			h.t.Errorf("the log is missing %q (is request logging on?):\n%s", want, logged)
		}
	}
}

// Browsing logs route patterns only: no item key, title, author or search text.
func TestWebPlayerBrowsingLogHasNoItemKey(t *testing.T) {
	h := newAudioHarness(t)
	_, kidC := h.rs.user(t, "kiddo", auth.RoleRequester)
	h.ok("GET", "/api/v1/me/audio/shelves", nil, kidC)
	h.ok("GET", "/api/v1/me/audio/library?q=crawler", nil, kidC)
	h.ok("GET", "/api/v1/me/audio/items/"+h.key, nil, kidC)
	h.req("GET", "/api/v1/me/audio/items/"+h.key+"/cover", nil, kidC, false)
	h.req("GET", "/api/v1/me/audio/items/b999", nil, kidC, false)
	h.noItemInLogs("kiddo", "GET /api/v1/me/audio/items/{key}", "GET /api/v1/me/audio/items/{key}/cover", "GET /api/v1/me/audio/library")
}

// play opens a web-player session and returns the reply.
func (h *audioHarness) play(c *http.Cookie) map[string]any {
	h.t.Helper()
	return h.ok("POST", "/api/v1/me/audio/items/"+h.key+"/play", map[string]any{"device_id": "tab-1"}, c)
}

// sync posts a web-player report (pos nil: not sent) and returns the HTTP reply.
func (h *audioHarness) sync(c *http.Cookie, sid, verb string, body any) *httptest.ResponseRecorder {
	h.t.Helper()
	return h.req("POST", "/api/v1/me/audio/sessions/"+sid+"/"+verb, body, c, false)
}

// The web player starts a session, syncs and closes it, under exactly the guards the
// apps get: a big jump back is held, and a report without a position never moves the
// place.
func TestWebPlayerPlaySyncClose(t *testing.T) {
	h := newAudioHarness(t)
	kid, kidC := h.rs.user(t, "kid", auth.RoleRequester)
	ctx := context.Background()
	start := h.play(kidC)
	sid, _ := start["session_id"].(string)
	if sid == "" || start["start_time"] != 0.0 || start["restart"] != false || len(start["tracks"].([]any)) != 2 {
		t.Fatalf("play = %v", start)
	}

	rec := h.sync(kidC, sid, "sync", map[string]any{"current_time": 600, "time_listened": 0})
	if res := decode(t, rec); rec.Code != 200 || res["position"] != 600.0 || res["held_position"] != nil {
		t.Fatalf("sync forward: HTTP %d %v", rec.Code, res)
	}
	// Far back: held, not saved — the same as an app's sync.
	rec = h.sync(kidC, sid, "sync", map[string]any{"current_time": 10, "time_listened": 0})
	if res := decode(t, rec); res["position"] != 600.0 || res["held_position"] != 10.0 {
		t.Fatalf("sync far back = %v, want 600 kept and 10 held", res)
	}
	// Closes that don't say where the player is: an empty body, null, {}.
	for _, body := range []any{nil, "null", "{}", map[string]any{"time_listened": 3}} {
		rec = h.sync(kidC, sid, "close", body)
		if res := decode(t, rec); rec.Code != 200 || res["position"] != 600.0 {
			t.Fatalf("close with %v: HTTP %d %v, want the place left at 600", body, rec.Code, res)
		}
	}
	p, _, _ := h.srv.Listen().Progress(ctx, kid.ID, h.key)
	if p.Position != 600 || p.PendingPosition == nil || *p.PendingPosition != 10 {
		t.Fatalf("place = %+v, want 600 with 10 held", p)
	}
	sess, err := h.srv.Listen().GetSession(ctx, kid.ID, sid)
	if err != nil || !sess.Closed || sess.Client != "Arrmada web" || !strings.HasPrefix(sess.Device, "Web player · ") {
		t.Fatalf("session = %+v %v", sess, err)
	}
	if rec := h.sync(kidC, "no-such-session", "sync", map[string]any{"current_time": 5}); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown session: HTTP %d", rec.Code)
	}
	if rec := h.sync(kidC, sid, "sync", "not json"); rec.Code != http.StatusBadRequest {
		t.Fatalf("garbage body: HTTP %d", rec.Code)
	}
}

// Audio streams with Range, so the player can seek.
func TestWebPlayerStreamsWithRange(t *testing.T) {
	h := newAudioHarness(t)
	_, kidC := h.rs.user(t, "kid", auth.RoleRequester)
	tracks := h.play(kidC)["tracks"].([]any)
	u := tracks[0].(map[string]any)["url"].(string)
	for _, external := range []bool{false, true} {
		rec := h.req("GET", u, nil, kidC, external, "Range", "bytes=0-9")
		if rec.Code != http.StatusPartialContent || rec.Header().Get("Content-Range") == "" || rec.Body.Len() != 10 {
			t.Fatalf("range (external %v): HTTP %d, Content-Range %q, %d bytes", external, rec.Code, rec.Header().Get("Content-Range"), rec.Body.Len())
		}
		if cc := rec.Header().Get("Cache-Control"); !strings.HasPrefix(cc, "private") {
			t.Fatalf("Cache-Control = %q, want private", cc)
		}
	}
	if rec := h.req("GET", "/api/v1/me/audio/items/"+h.key+"/file/nope", nil, kidC, false); rec.Code != http.StatusNotFound {
		t.Fatalf("unknown file: HTTP %d", rec.Code)
	}
	if rec := h.req("GET", u, nil, nil, false); rec.Code != http.StatusUnauthorized {
		t.Fatalf("signed out: HTTP %d", rec.Code)
	}
}

// One person's session id is no use to anyone else.
func TestWebPlayerCannotTouchOthersSessions(t *testing.T) {
	h := newAudioHarness(t)
	kid, kidC := h.rs.user(t, "kid", auth.RoleRequester)
	_, momC := h.rs.user(t, "mom", auth.RoleRequester)
	_, adminC := h.rs.user(t, "boss", auth.RoleAdmin)
	sid := h.play(kidC)["session_id"].(string)
	h.sync(kidC, sid, "sync", map[string]any{"current_time": 300})
	for _, c := range []*http.Cookie{momC, adminC} {
		for _, verb := range []string{"sync", "close"} {
			if rec := h.sync(c, sid, verb, map[string]any{"current_time": 5}); rec.Code != http.StatusNotFound {
				t.Fatalf("%s someone else's session: HTTP %d, want 404", verb, rec.Code)
			}
		}
	}
	if p, _, _ := h.srv.Listen().Progress(context.Background(), kid.ID, h.key); p.Position != 300 {
		t.Fatalf("the kid's place moved to %v", p.Position)
	}
}

// Bookmarks: add (a second one at the same second renames it), list, remove.
func TestWebPlayerBookmarks(t *testing.T) {
	h := newAudioHarness(t)
	_, kidC := h.rs.user(t, "kid", auth.RoleRequester)
	_, momC := h.rs.user(t, "mom", auth.RoleRequester)
	base := "/api/v1/me/audio/items/" + h.key + "/bookmarks"
	if b := h.ok("POST", base, map[string]any{"time": 300.7, "title": "Good bit"}, kidC); b["time"] != 300.0 || b["title"] != "Good bit" {
		t.Fatalf("add = %v", b)
	}
	h.ok("POST", base, map[string]any{"time": 300.2, "title": "Best bit"}, kidC)
	h.ok("POST", base, map[string]any{"time": 900, "title": ""}, kidC)
	list := h.ok("GET", base, nil, kidC)["bookmarks"].([]any)
	if len(list) != 2 || list[0].(map[string]any)["title"] != "Best bit" {
		t.Fatalf("bookmarks = %v", list)
	}
	if mine := h.ok("GET", base, nil, momC)["bookmarks"].([]any); len(mine) != 0 {
		t.Fatalf("another account sees the kid's bookmarks: %v", mine)
	}
	if rec := h.req("DELETE", base+"/300", nil, kidC, false); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: HTTP %d %s", rec.Code, rec.Body)
	}
	if list := h.ok("GET", base, nil, kidC)["bookmarks"].([]any); len(list) != 1 {
		t.Fatalf("after delete = %v", list)
	}
	if rec := h.req("POST", base, map[string]any{"time": -1}, kidC, false); rec.Code != http.StatusBadRequest {
		t.Fatalf("negative time: HTTP %d", rec.Code)
	}
	if rec := h.req("POST", "/api/v1/me/audio/items/b999/bookmarks", map[string]any{"time": 1}, kidC, false); rec.Code != http.StatusNotFound {
		t.Fatalf("bookmark on an unknown item: HTTP %d", rec.Code)
	}
}

// A whole listening session in the web player: the admin's Listening overview shows
// when and how long on "Web player", never the book — and no log line names it either.
func TestWebPlayerSessionInListenLogWithoutTitle(t *testing.T) {
	h := newAudioHarness(t)
	_, kidC := h.rs.user(t, "kiddo", auth.RoleRequester)
	_, adminC := h.rs.user(t, "boss", auth.RoleAdmin)
	h.ok("GET", "/api/v1/me/audio/shelves", nil, kidC)
	h.ok("GET", "/api/v1/me/audio/library?q=crawler", nil, kidC)
	h.ok("GET", "/api/v1/me/audio/items/"+h.key, nil, kidC)
	start := h.req("POST", "/api/v1/me/audio/items/"+h.key+"/play", map[string]any{"device_id": "tab-1"}, kidC, true,
		"User-Agent", "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1")
	if start.Code != 200 {
		t.Fatalf("play: HTTP %d %s", start.Code, start.Body)
	}
	sid := decode(t, start)["session_id"].(string)
	u := decode(t, start)["tracks"].([]any)[0].(map[string]any)["url"].(string)
	h.req("GET", u, nil, kidC, true, "Range", "bytes=0-99")
	h.sync(kidC, sid, "sync", map[string]any{"current_time": 40, "time_listened": 10})
	h.sync(kidC, sid, "sync", map[string]any{"current_time": 2, "time_listened": 10}) // held: logs nothing about it
	h.ok("POST", "/api/v1/me/audio/items/"+h.key+"/bookmarks", map[string]any{"time": 30, "title": "Good bit"}, kidC)
	h.req("DELETE", "/api/v1/me/audio/items/"+h.key+"/bookmarks/30", nil, kidC, false)
	h.sync(kidC, sid, "close", nil)
	h.ok("GET", "/api/v1/me/audio", nil, kidC)

	log := h.ok("GET", "/api/v1/audioserver/listening", nil, adminC)
	raw, _ := json.Marshal(log)
	for _, secret := range []string{h.key, h.title, "Dinniman", "Good bit"} {
		if strings.Contains(string(raw), secret) {
			t.Errorf("the admin's listening overview contains %q: %s", secret, raw)
		}
	}
	// The listening log has the session as the web player's, with no book column at all.
	// (Under 30 s, the overview itself leaves it out.)
	var rows int
	if err := h.rs.st.DB().QueryRow(`SELECT COUNT(*) FROM listen_log WHERE device = ? AND client = 'Arrmada web' AND seconds > 0`,
		"Web player · Safari on iPhone").Scan(&rows); err != nil || rows != 1 {
		t.Fatalf("web-player rows in the listening log = %d %v, want 1", rows, err)
	}
	h.noItemInLogs("kiddo",
		"POST /api/v1/me/audio/items/{key}/play",
		"POST /api/v1/me/audio/sessions/{sid}/sync",
		"POST /api/v1/me/audio/sessions/{sid}/close",
		"GET /api/v1/me/audio/items/{key}/file/{ino}",
		"DELETE /api/v1/me/audio/items/{key}/bookmarks/{time}")
}
