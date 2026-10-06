package audioserver

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
	"testing"

	"github.com/tristenlammi/arrmada/internal/auth"
	"github.com/tristenlammi/arrmada/internal/books"
	"github.com/tristenlammi/arrmada/internal/listening"
	"github.com/tristenlammi/arrmada/internal/metadata"
	"github.com/tristenlammi/arrmada/internal/settings"
	"github.com/tristenlammi/arrmada/internal/store"
)

type harness struct {
	t     *testing.T
	srv   *Server
	http  *httptest.Server
	users *auth.Service
	book  books.Book
	token string
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	db := st.DB()
	users := auth.NewService(db)
	ctx := context.Background()
	if _, err := users.CreateUser(ctx, "reader", "correct-horse-1", auth.RoleRequester, false); err != nil {
		t.Fatal(err)
	}
	if _, err := users.CreateUser(ctx, "viewer", "correct-horse-2", auth.RoleReadonly, false); err != nil {
		t.Fatal(err)
	}
	bs := books.NewService(db, nil, slog.Default())
	added, _ := bs.AddWorks(ctx, []metadata.BookResult{{Key: "hc:1", Title: "Dungeon Crawler Carl", Author: "Matt Dinniman"}}, "", true)
	if len(added) != 1 {
		t.Fatal("book not added")
	}
	audio := filepath.Join(dir, "lib", "Matt Dinniman", "Dungeon Crawler Carl")
	_ = os.MkdirAll(audio, 0o755)
	for _, n := range []string{"Part 10.mp3", "Part 2.mp3", "Part 1.mp3"} {
		if err := os.WriteFile(filepath.Join(audio, n), bytes.Repeat([]byte(n[:6]), 2000), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := bs.MarkImported(ctx, added[0].ID, books.KindAudiobook, audio, "MP3", 36000, 3); err != nil {
		t.Fatal(err)
	}
	b, _ := bs.Get(ctx, added[0].ID)
	s := New(Options{DB: db, Books: bs, Listen: listening.NewStore(db), Users: users, Settings: settings.NewService(db),
		Log: slog.Default(), FFprobe: "ffprobe-not-installed", DataDir: dir})
	for name, pw := range map[string]string{"reader": "listen-pass-1", "viewer": "listen-pass-2"} {
		u, _ := users.UserByUsername(ctx, name)
		if err := s.Accounts.SetPassword(ctx, u.ID, pw, false); err != nil {
			t.Fatal(err)
		}
	}
	h := &harness{t: t, srv: s, http: httptest.NewServer(s.Handler()), users: users, book: b}
	t.Cleanup(h.http.Close)
	return h
}

func (h *harness) do(method, path string, body any, hdr map[string]string) (int, []byte) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.http.URL+path, rd)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "Lissen/1.9 (Android 15)")
	if h.token != "" {
		req.Header.Set("Authorization", "Bearer "+h.token)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, out
}

func (h *harness) json(method, path string, body any) map[string]any {
	h.t.Helper()
	code, out := h.do(method, path, body, nil)
	if code != 200 {
		h.t.Fatalf("%s %s: HTTP %d %s", method, path, code, out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		h.t.Fatalf("%s %s: not a JSON object: %s", method, path, out)
	}
	return m
}

// need checks fields Lissen's models declare non-null are present and not null.
func need(t *testing.T, where string, m map[string]any, keys ...string) {
	t.Helper()
	for _, k := range keys {
		if v, ok := m[k]; !ok || v == nil {
			t.Errorf("%s: missing required field %q", where, k)
		}
	}
}

func obj1(t *testing.T, v any) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("expected an object, got %T", v)
	}
	return m
}

func list1(t *testing.T, v any) []any {
	t.Helper()
	l, ok := v.([]any)
	if !ok {
		t.Fatalf("expected a list, got %T", v)
	}
	return l
}

// The whole Lissen conversation, checked against its models.
func TestLissenConversation(t *testing.T) {
	h := newHarness(t)
	key := itemKeyFor(h.book.ID, 0)

	st := h.json("GET", "/status", nil)
	if list1(t, st["authMethods"])[0] != "local" {
		t.Fatalf("status authMethods: %v", st["authMethods"])
	}

	// Wrong password, then a read-only account, then the real sign-in.
	if code, _ := h.do("POST", "/login", map[string]string{"username": "reader", "password": "nope"}, nil); code != 401 {
		t.Fatalf("wrong password: HTTP %d", code)
	}
	if code, _ := h.do("POST", "/login", map[string]string{"username": "viewer", "password": "listen-pass-2"}, nil); code != 403 {
		t.Fatalf("read-only account: HTTP %d, want 403", code)
	}
	code, out := h.do("POST", "/login", map[string]string{"username": "reader", "password": "listen-pass-1"}, map[string]string{"x-return-tokens": "true"})
	if code != 200 {
		t.Fatalf("login: HTTP %d %s", code, out)
	}
	var login map[string]any
	_ = json.Unmarshal(out, &login)
	user := obj1(t, login["user"])
	need(t, "login.user", user, "id", "username", "token", "accessToken", "refreshToken")
	if login["userDefaultLibraryId"] != libraryID {
		t.Fatalf("userDefaultLibraryId = %v", login["userDefaultLibraryId"])
	}
	h.token = user["accessToken"].(string)
	refresh := user["refreshToken"].(string)

	auth := h.json("POST", "/api/authorize", nil)
	need(t, "authorize.user", obj1(t, auth["user"]), "username")
	need(t, "authorize.serverSettings", obj1(t, auth["serverSettings"]), "version")

	libs := h.json("GET", "/api/libraries", nil)
	need(t, "library", obj1(t, list1(t, libs["libraries"])[0]), "id", "name", "mediaType")

	lib := h.json("GET", "/api/libraries/"+libraryID+"?include=filterdata", nil)
	need(t, "library.filterdata", obj1(t, lib["filterdata"]), "authors", "genres", "tags", "series")

	items := h.json("GET", "/api/libraries/"+libraryID+"/items?limit=10&page=0&sort=media.metadata.title&desc=0&minified=1&filter=progress.bm90LWZpbmlzaGVk&collapseseries=0", nil)
	need(t, "items", items, "results", "page", "total")
	res := list1(t, items["results"])
	if len(res) != 1 {
		t.Fatalf("items results = %d, want 1", len(res))
	}
	it := obj1(t, res[0])
	need(t, "items[0]", it, "id", "media")
	need(t, "items[0].media", obj1(t, it["media"]), "metadata")

	book := h.json("GET", "/api/items/"+key, nil)
	need(t, "item", book, "id", "ino", "libraryId", "media", "addedAt", "ctimeMs")
	media := obj1(t, book["media"])
	need(t, "item.media", media, "metadata", "audioFiles", "chapters")
	need(t, "item.media.metadata", obj1(t, media["metadata"]), "title")
	files := list1(t, media["audioFiles"])
	if len(files) != 3 {
		t.Fatalf("audio files = %d, want 3", len(files))
	}
	f0 := obj1(t, files[0])
	need(t, "audioFile", f0, "index", "ino", "metadata", "mimeType")
	need(t, "audioFile.metadata", obj1(t, f0["metadata"]), "filename", "ext", "size")
	if name := obj1(t, f0["metadata"])["filename"]; name != "Part 1.mp3" {
		t.Fatalf("first file %v — files must be in natural order (Part 1, 2, 10)", name)
	}
	if obj1(t, obj1(t, files[2])["metadata"])["filename"] != "Part 10.mp3" {
		t.Fatal("Part 10 must come last")
	}

	play := h.json("POST", "/api/items/"+key+"/play", map[string]any{
		"deviceInfo":         map[string]string{"clientName": "Lissen", "deviceId": "dev-1", "deviceName": "Pixel 8"},
		"supportedMimeTypes": []string{"audio/mpeg"}, "mediaPlayer": "exo", "forceTranscode": false, "forceDirectPlay": true,
	})
	need(t, "play", play, "id", "libraryItemId", "audioTracks")
	sid := play["id"].(string)

	if code, out := h.do("POST", "/api/session/"+sid+"/sync", map[string]float64{"timeListened": 0, "currentTime": 120}, nil); code != 200 {
		t.Fatalf("sync: HTTP %d %s", code, out)
	}
	prog := h.json("GET", "/api/me/progress/"+key, nil)
	need(t, "progress", prog, "libraryItemId", "currentTime", "isFinished", "lastUpdate", "progress")
	if prog["currentTime"].(float64) != 120 {
		t.Fatalf("progress currentTime = %v", prog["currentTime"])
	}

	// Streaming, with a Range request (seeking).
	ino := f0["ino"].(string)
	if code, _ := h.do("GET", "/api/items/"+key+"/file/"+ino, nil, nil); code != 200 {
		t.Fatalf("stream: HTTP %d", code)
	}
	if code, _ := h.do("GET", "/api/items/"+key+"/file/"+ino, nil, map[string]string{"Range": "bytes=100-199"}); code != 206 {
		t.Fatalf("range request: HTTP %d, want 206", code)
	}

	// Offline listening uploaded later.
	local := h.json("POST", "/api/session/local-all", map[string]any{
		"deviceInfo": map[string]string{"clientName": "Lissen", "deviceId": "dev-1", "deviceName": "Pixel 8"},
		"sessions": []map[string]any{{"id": "off-1", "libraryItemId": key, "mediaType": "book", "displayTitle": "x",
			"duration": 36000, "playMethod": 3, "mediaPlayer": "exo", "deviceInfo": map[string]string{"clientName": "Lissen", "deviceId": "dev-1", "deviceName": "Pixel 8"},
			"date": "2026-09-01", "dayOfWeek": "Tuesday", "startTime": 120, "currentTime": 900, "timeListening": 780,
			"startedAt": 1, "updatedAt": 9_999_999_999_999}},
	})
	r0 := obj1(t, list1(t, local["results"])[0])
	if r0["id"] != "off-1" || r0["success"] != true {
		t.Fatalf("local-all result: %v", r0)
	}

	// Bookmarks and /api/me.
	bm := h.json("POST", "/api/me/item/"+key+"/bookmark", map[string]any{"time": 300, "title": "Good bit"})
	need(t, "bookmark", bm, "libraryItemId", "time", "title", "createdAt")
	me := h.json("GET", "/api/me", nil)
	if len(list1(t, me["mediaProgress"])) != 1 || len(list1(t, me["bookmarks"])) != 1 {
		t.Fatalf("/api/me progress/bookmarks: %v %v", me["mediaProgress"], me["bookmarks"])
	}
	if code, _ := h.do("DELETE", "/api/me/item/"+key+"/bookmark/300", nil, nil); code != 200 {
		t.Fatal("bookmark delete failed")
	}

	// Home shelves, authors, search, batch.
	code, out = h.do("GET", "/api/libraries/"+libraryID+"/personalized", nil, nil)
	var shelves []map[string]any
	if code != 200 || json.Unmarshal(out, &shelves) != nil || len(shelves) == 0 {
		t.Fatalf("personalized: %d %s", code, out)
	}
	need(t, "shelf", shelves[0], "id", "labelStringKey", "entities")
	if shelves[0]["id"] != "continue-listening" {
		t.Fatalf("first shelf is %v, want continue-listening", shelves[0]["id"])
	}
	need(t, "shelf entity", obj1(t, list1(t, shelves[0]["entities"])[0]), "id", "libraryId")
	authors := h.json("GET", "/api/libraries/"+libraryID+"/authors?limit=20&page=0&sort=name&desc=0", nil)
	need(t, "authors", authors, "results", "page", "total")
	need(t, "author", obj1(t, list1(t, authors["results"])[0]), "id", "name")
	search := h.json("GET", "/api/libraries/"+libraryID+"/search?q=crawler&limit=10", nil)
	need(t, "search", search, "book", "authors", "series")
	if len(list1(t, search["book"])) != 1 {
		t.Fatalf("search found %v", search["book"])
	}
	batch := h.json("POST", "/api/items/batch/get", map[string]any{"libraryItemIds": []string{key, "b999"}})
	if len(list1(t, batch["libraryItems"])) != 1 {
		t.Fatalf("batch: %v", batch["libraryItems"])
	}
	authorID := obj1(t, list1(t, authors["results"])[0])["id"].(string)
	ai := h.json("GET", "/api/authors/"+authorID+"?include=items", nil)
	if len(list1(t, ai["libraryItems"])) != 1 {
		t.Fatalf("author items: %v", ai["libraryItems"])
	}

	// Refresh gives a working access token; the old refresh token keeps working.
	code, out = h.do("POST", "/auth/refresh", nil, map[string]string{"x-refresh-token": refresh, "x-return-tokens": "true"})
	if code != 200 {
		t.Fatalf("refresh: HTTP %d %s", code, out)
	}
	var ref map[string]any
	_ = json.Unmarshal(out, &ref)
	h.token = obj1(t, ref["user"])["accessToken"].(string)
	h.json("GET", "/api/me", nil)
	if code, _ := h.do("POST", "/auth/refresh", nil, map[string]string{"x-refresh-token": refresh}); code != 200 {
		t.Fatal("refresh token must survive a refresh (no rotation)")
	}

	// Revoking the device signs it out.
	devs, _ := h.srv.Accounts.Devices(context.Background(), 0)
	if len(devs) != 1 || devs[0].Device != "Pixel 8" {
		t.Fatalf("devices: %+v", devs)
	}
	_ = h.srv.Accounts.RevokeFamily(context.Background(), devs[0].Family, 0)
	if code, _ := h.do("GET", "/api/me", nil, nil); code != 401 {
		t.Fatalf("revoked device still signed in: HTTP %d", code)
	}
}

// Only the audiobook password signs in — never the Arrmada one, and nobody without an
// audiobook password. Changing it (with sign-out) or removing it signs devices out, and
// an admin switching a user off locks them out.
func TestAudioPasswordAndAccess(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	u, _ := h.users.UserByUsername(ctx, "reader")
	login := func(user, pw string) (int, string) {
		code, out := h.do("POST", "/login", map[string]string{"username": user, "password": pw}, nil)
		if code != 200 {
			return code, ""
		}
		var l map[string]any
		_ = json.Unmarshal(out, &l)
		return code, obj1(t, l["user"])["accessToken"].(string)
	}

	if code, _ := login("reader", "correct-horse-1"); code != 401 {
		t.Fatalf("Arrmada password accepted: HTTP %d", code)
	}
	if err := h.srv.Accounts.RemovePassword(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := login("reader", "listen-pass-1"); code != 401 {
		t.Fatalf("login with no audiobook password set: HTTP %d", code)
	}
	if err := h.srv.Accounts.SetPassword(ctx, u.ID, "short", false); err == nil {
		t.Fatal("a too-short password was accepted")
	}
	if err := h.srv.Accounts.SetPassword(ctx, u.ID, "listen-pass-1", false); err != nil {
		t.Fatal(err)
	}
	code, tok := login("READER", "listen-pass-1") // usernames aren't case-sensitive
	if code != 200 {
		t.Fatalf("audiobook password login: HTTP %d", code)
	}
	h.token = tok
	h.json("GET", "/api/me", nil)

	// Changing it without sign-out keeps the phone; with sign-out drops it.
	if err := h.srv.Accounts.SetPassword(ctx, u.ID, "listen-pass-2", false); err != nil {
		t.Fatal(err)
	}
	h.json("GET", "/api/me", nil)
	if code, _ := login("reader", "listen-pass-1"); code != 401 {
		t.Fatalf("old password still works: HTTP %d", code)
	}
	if err := h.srv.Accounts.SetPassword(ctx, u.ID, "listen-pass-3", true); err != nil {
		t.Fatal(err)
	}
	if code, _ := h.do("GET", "/api/me", nil, nil); code != 401 {
		t.Fatalf("device survived a password change with sign-out: HTTP %d", code)
	}

	// Removing the password signs out too.
	_, h.token = login("reader", "listen-pass-3")
	h.json("GET", "/api/me", nil)
	if err := h.srv.Accounts.RemovePassword(ctx, u.ID); err != nil {
		t.Fatal(err)
	}
	if code, _ := h.do("GET", "/api/me", nil, nil); code != 401 {
		t.Fatalf("device survived removing the password: HTTP %d", code)
	}

	_ = h.srv.Accounts.SetPassword(ctx, u.ID, "listen-pass-4", false)
	_, h.token = login("reader", "listen-pass-4")
	_ = h.srv.SetAllowed(ctx, u.ID, false)
	if code, _ := h.do("GET", "/api/me", nil, nil); code != 401 {
		t.Fatalf("switched-off user still has access: HTTP %d", code)
	}
}

// Proxy headers only count from a local proxy (the Cloudflare tunnel); from anywhere
// else they could be forged to dodge the login limit.
func TestClientIP(t *testing.T) {
	for _, c := range []struct{ remote, cf, want string }{
		{"127.0.0.1:5000", "203.0.113.9", "203.0.113.9"},
		{"172.18.0.4:5000", "203.0.113.9", "203.0.113.9"},
		{"198.51.100.7:5000", "203.0.113.9", "198.51.100.7"},
		{"198.51.100.7:5000", "", "198.51.100.7"},
	} {
		r := httptest.NewRequest("POST", "/login", nil)
		r.RemoteAddr = c.remote
		if c.cf != "" {
			r.Header.Set("Cf-Connecting-Ip", c.cf)
		}
		if got := clientIP(r); got != c.want {
			t.Errorf("clientIP(%s, cf=%q) = %s, want %s", c.remote, c.cf, got, c.want)
		}
	}
}

// Guessing one account's password is limited however many addresses take turns.
func TestLoginLimitPerAccount(t *testing.T) {
	h := newHarness(t)
	for i := 0; i < 10; i++ {
		if code, _ := h.do("POST", "/login", map[string]string{"username": "reader", "password": "wrong"},
			map[string]string{"Cf-Connecting-Ip": fmt.Sprintf("203.0.113.%d", i)}); code != 401 {
			t.Fatalf("attempt %d: HTTP %d", i, code)
		}
	}
	if code, _ := h.do("POST", "/login", map[string]string{"username": "reader", "password": "listen-pass-1"},
		map[string]string{"Cf-Connecting-Ip": "203.0.113.200"}); code != 429 {
		t.Fatalf("11th attempt on one account from a new address: HTTP %d, want 429", code)
	}
}

// Routes newer Audiobookshelf clients (Plappa) call while loading answer in the right
// shape, and the expanded item is a superset of the list item, as Audiobookshelf 2.36+
// promises.
func TestNewerClientRoutes(t *testing.T) {
	h := newHarness(t)
	key := itemKeyFor(h.book.ID, 0)
	_, out := h.do("POST", "/login", map[string]string{"username": "reader", "password": "listen-pass-1"}, nil)
	var login map[string]any
	_ = json.Unmarshal(out, &login)
	h.token = obj1(t, login["user"])["accessToken"].(string)

	h.json("PATCH", "/api/me/progress/"+key, map[string]any{"currentTime": 120, "duration": 36000})
	h.json("POST", "/api/me/item/"+key+"/bookmark", map[string]any{"time": 60, "title": "Mordecai"})
	if mp := list1(t, h.json("GET", "/api/me/progress", nil)["mediaProgress"]); len(mp) != 1 || obj1(t, mp[0])["libraryItemId"] != key {
		t.Fatalf("/api/me/progress = %v", mp)
	}
	if bm := list1(t, h.json("GET", "/api/me/bookmarks", nil)["bookmarks"]); len(bm) != 1 {
		t.Fatalf("/api/me/bookmarks = %v", bm)
	}
	if bm := list1(t, h.json("GET", "/api/me/bookmarks/"+key, nil)["bookmarks"]); len(bm) != 1 {
		t.Fatalf("/api/me/bookmarks/{id} = %v", bm)
	}
	for _, p := range []string{"/api/me/listening-sessions?itemsPerPage=10&page=0", "/api/me/item/listening-sessions/" + key, "/api/me/sessions"} {
		need(t, p, h.json("GET", p, nil), "sessions", "total", "numPages", "itemsPerPage")
	}
	lib := "/api/libraries/" + libraryID
	for _, p := range []string{lib + "/collections", lib + "/playlists", lib + "/recent-episodes"} {
		need(t, p, h.json("GET", p, nil), "results", "total")
	}
	need(t, "narrators", h.json("GET", lib+"/narrators", nil), "narrators")
	need(t, "stats", h.json("GET", lib+"/stats", nil), "totalItems", "totalDuration", "totalSize")
	need(t, "collections", h.json("GET", "/api/collections", nil), "collections")
	need(t, "playlists", h.json("GET", "/api/playlists", nil), "playlists")
	h.json("GET", "/api/me/series/x/remove-from-continue-listening", nil)

	list := obj1(t, list1(t, h.json("GET", lib+"/items?limit=10&page=0", nil)["results"])[0])
	full := h.json("GET", "/api/items/"+key+"?expanded=1", nil)
	for k := range list {
		if _, ok := full[k]; !ok && k != "userMediaProgress" {
			t.Errorf("expanded item lacks list field %q", k)
		}
	}
	fullMedia := obj1(t, full["media"])
	for k := range obj1(t, list["media"]) {
		if _, ok := fullMedia[k]; !ok {
			t.Errorf("expanded media lacks list field %q", k)
		}
	}
	for k := range obj1(t, obj1(t, list["media"])["metadata"]) {
		if _, ok := obj1(t, fullMedia["metadata"])[k]; !ok {
			t.Errorf("expanded metadata lacks list field %q", k)
		}
	}

	if code, _ := h.do("GET", "/api/no-such-route", nil, nil); code != 404 {
		t.Fatalf("unknown route: HTTP %d", code)
	}
}
