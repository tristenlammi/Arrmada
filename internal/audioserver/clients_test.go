package audioserver

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Conversations of the open-source iPhone apps, taken from their source rather than a
// device: the requests each one makes, in order, and the reply fields it can't read the
// reply without (Swift's decoder drops a whole reply over one missing or mistyped
// field). They don't prove an app works on a phone — the owner's run-through does — but
// they fail if a reply regresses in a way that app would trip on.
//
// ShelfPlayer: rasmuslos/ShelfPlayer, branch macOS @ fe23d50 (July 2025, the last public
// source; the App Store app has since been sold and its code is closed).
// Official app: advplyr/audiobookshelf-app v0.14.2 @ 7014e04 (iOS native player + Vue).

// fieldKinds checks each field is present with the JSON kind a Swift model declares:
// "string", "number", "int" (a whole number — Swift's Int rejects 1.5), "bool", "array",
// "object". A "?" suffix allows the field to be missing or null.
func fieldKinds(t *testing.T, where string, m map[string]any, want map[string]string) {
	t.Helper()
	for k, kind := range want {
		opt := strings.HasSuffix(kind, "?")
		kind = strings.TrimSuffix(kind, "?")
		v, ok := m[k]
		if !ok || v == nil {
			if !opt {
				t.Errorf("%s: %q is required (%s), got %v", where, k, kind, v)
			}
			continue
		}
		got := jsonKind(v)
		switch kind {
		case "int":
			if f, isNum := v.(float64); !isNum || f != math.Trunc(f) {
				t.Errorf("%s: %q must be a whole number, got %v", where, k, v)
			}
		default:
			if got != kind {
				t.Errorf("%s: %q must be %s, got %s (%v)", where, k, kind, got, v)
			}
		}
	}
}

// do with a raw query string (a path built here, not by net/url) and a User-Agent.
func (h *harness) doUA(ua, method, path string, body any, hdr map[string]string) (int, []byte) {
	h.t.Helper()
	all := map[string]string{"User-Agent": ua}
	for k, v := range hdr {
		all[k] = v
	}
	return h.do(method, path, body, all)
}

func (h *harness) jsonUA(ua, method, path string, body any) map[string]any {
	h.t.Helper()
	code, out := h.doUA(ua, method, path, body, nil)
	if code != 200 {
		h.t.Fatalf("%s %s: HTTP %d %s", method, path, code, out)
	}
	var m map[string]any
	if err := json.Unmarshal(out, &m); err != nil {
		h.t.Fatalf("%s %s: not a JSON object: %s", method, path, out)
	}
	return m
}

// checkTracks checks play tracks the way both iPhone apps read them: each has the fields
// they decode, a contentUrl that's a plain path (ShelfPlayer appends it with
// URL.appending(path:), which would escape a "?"), and the tracks run end to end from 0
// (ShelfPlayer force-unwraps the track index for a position, and crashes on a gap).
func checkTracks(t *testing.T, where string, tracks []any) {
	t.Helper()
	if len(tracks) == 0 {
		t.Fatalf("%s: no tracks", where)
	}
	end := 0.0
	for i, tr := range tracks {
		m := obj1(t, tr)
		fieldKinds(t, fmt.Sprintf("%s[%d]", where, i), m, map[string]string{
			"startOffset": "number", "duration": "number", "contentUrl": "string", "mimeType": "string",
			"index": "int", "ino": "string", "metadata": "object", "title": "string?",
		})
		if u, _ := m["contentUrl"].(string); strings.ContainsAny(u, "?#") || !strings.HasPrefix(u, "/api/items/") {
			t.Errorf("%s[%d]: contentUrl %q must be a plain path", where, i, u)
		}
		if math.Abs(m["startOffset"].(float64)-end) > 1e-6 {
			t.Errorf("%s[%d]: starts at %v, the track before ends at %v", where, i, m["startOffset"], end)
		}
		end = m["startOffset"].(float64) + m["duration"].(float64)
		fieldKinds(t, fmt.Sprintf("%s[%d].metadata", where, i), obj1(t, m["metadata"]), map[string]string{
			"ext": "string", "filename": "string", "path": "string", "relPath": "string", "size": "number",
		})
	}
}

func checkChapters(t *testing.T, where string, chapters []any) {
	t.Helper()
	for i, c := range chapters {
		fieldKinds(t, fmt.Sprintf("%s[%d]", where, i), obj1(t, c), map[string]string{
			"id": "int", "start": "number", "end": "number", "title": "string"})
	}
}

// checkMetadata: ShelfPlayer requires genres everywhere (even on a session's
// mediaMetadata) and reads strings strictly; the official app requires title and a real
// boolean explicit.
func checkMetadata(t *testing.T, where string, m map[string]any) {
	t.Helper()
	fieldKinds(t, where, m, map[string]string{
		"title": "string", "genres": "array", "explicit": "bool", "abridged": "bool?", "authorName": "string?",
		"narratorName": "string?", "seriesName": "string?", "publishedYear": "string?", "subtitle": "string?",
		"isbn": "string?", "asin": "string?", "language": "string?", "description": "string?",
	})
}

func checkProgress(t *testing.T, where string, p map[string]any) {
	t.Helper()
	// Both apps: ShelfPlayer's ProgressPayload and the official app's MediaProgress.
	fieldKinds(t, where, p, map[string]string{
		"id": "string", "userId": "string", "libraryItemId": "string", "isFinished": "bool", "duration": "number",
		"progress": "number", "currentTime": "number", "lastUpdate": "int", "startedAt": "int", "finishedAt": "int?",
		"hideFromContinueListening": "bool?", "episodeId": "string?",
	})
}

const shelfPlayerUA = "ShelfPlayer/3.3.0 CFNetwork/3826.500.131 Darwin/24.5.0"

// ShelfPlayer: sign in with the long-lived token (it never refreshes), the sync gate
// (/api/authorize must decode before the library shows), browse, open, play, sync,
// close, mark finished and unfinished through batch/update, bookmark, and an offline
// session uploaded through /api/session/local.
func TestShelfPlayerConversation(t *testing.T) {
	h := shapeHarnessOnly(t)
	key := itemKeyFor(h.book.ID, 0)
	ua := shelfPlayerUA

	st := h.jsonUA(ua, "GET", "/status", nil)
	fieldKinds(t, "status", st, map[string]string{"isInit": "bool", "authMethods": "array", "serverVersion": "string"})
	if st["isInit"] != true || list1(t, st["authMethods"])[0] != "local" {
		t.Fatalf("status: %v", st)
	}

	// No x-return-tokens: it keeps user.token for good.
	login := h.jsonUA(ua, "POST", "/login", map[string]string{"username": "reader", "password": "listen-pass-1"})
	u := obj1(t, login["user"])
	fieldKinds(t, "login.user", u, map[string]string{"id": "string", "token": "string", "username": "string",
		"bookmarks": "array", "mediaProgress": "array"})
	h.token = u["token"].(string)

	libs := list1(t, h.jsonUA(ua, "GET", "/api/libraries", nil)["libraries"])
	lib := obj1(t, libs[0])
	fieldKinds(t, "library", lib, map[string]string{"id": "string", "name": "string", "mediaType": "string", "displayOrder": "int"})
	if lib["mediaType"] != "book" { // anything but book/podcast is a fatalError
		t.Fatalf("library mediaType %v", lib["mediaType"])
	}
	libID := lib["id"].(string)

	// The sync gate.
	auth := obj1(t, h.jsonUA(ua, "POST", "/api/authorize", nil)["user"])
	fieldKinds(t, "authorize.user", auth, map[string]string{"id": "string", "token": "string", "username": "string",
		"bookmarks": "array", "mediaProgress": "array"})

	// Library size, then the books list as it pages it.
	count := h.jsonUA(ua, "GET", "/api/libraries/"+libID+"/items?sort=addedAt&desc=1&collapseseries=0&page=0&limit=1", nil)
	fieldKinds(t, "items", count, map[string]string{"total": "int", "results": "array"})
	items := h.jsonUA(ua, "GET", "/api/libraries/"+libID+"/items?sort=media.metadata.title&desc=0&collapseseries=0&page=0&limit=100", nil)
	res := list1(t, items["results"])
	if len(res) != 1 {
		t.Fatalf("books: %d", len(res))
	}
	it := obj1(t, res[0])
	fieldKinds(t, "item", it, map[string]string{"id": "string", "libraryId": "string", "addedAt": "number", "media": "object"})
	media := obj1(t, it["media"])
	checkMetadata(t, "item.media.metadata", obj1(t, media["metadata"]))
	// A book with no audio files is dropped from the list.
	if n, _ := media["numAudioFiles"].(float64); n < 1 {
		t.Fatalf("numAudioFiles %v: ShelfPlayer would drop the book", media["numAudioFiles"])
	}

	// Home: rows of type book (or authors), every entity decodable.
	code, out := h.doUA(ua, "GET", "/api/libraries/"+libID+"/personalized", nil, nil)
	var rows []map[string]any
	if code != 200 || json.Unmarshal(out, &rows) != nil {
		t.Fatalf("personalized: %d %s", code, out)
	}
	for _, row := range rows {
		fieldKinds(t, "home row", row, map[string]string{"id": "string", "label": "string", "type": "string", "entities": "array"})
		for _, e := range list1(t, row["entities"]) {
			checkMetadata(t, "home entity metadata", obj1(t, obj1(t, obj1(t, e)["media"])["metadata"]))
		}
	}

	authors := h.jsonUA(ua, "GET", "/api/libraries/"+libID+"/authors?sort=name&desc=0&limit=100&page=0", nil)
	fieldKinds(t, "authors", authors, map[string]string{"total": "int", "results": "array"})
	a0 := obj1(t, list1(t, authors["results"])[0])
	fieldKinds(t, "author", a0, map[string]string{"id": "string", "name": "string", "libraryId": "string"}) // force-unwrapped
	series := h.jsonUA(ua, "GET", "/api/libraries/"+libID+"/series?sort=name&desc=0&filter=all&page=0&limit=100", nil)
	s0 := obj1(t, list1(t, series["results"])[0])
	fieldKinds(t, "series", s0, map[string]string{"id": "string", "name": "string"})
	one := h.jsonUA(ua, "GET", "/api/libraries/"+libID+"/series/"+s0["id"].(string), nil)
	fieldKinds(t, "series detail", one, map[string]string{"name": "string", "books": "array", "addedAt": "number"})
	h.jsonUA(ua, "GET", "/api/libraries/"+libID+"/search?q=crawler", nil)
	h.jsonUA(ua, "GET", "/api/libraries/"+libID+"/narrators", nil)
	h.jsonUA(ua, "GET", "/api/libraries/"+libID+"?include=filterdata", nil)

	// Book detail (and what a download reads).
	detail := h.jsonUA(ua, "GET", "/api/items/"+key+"?expanded=1", nil)
	dm := obj1(t, detail["media"])
	checkTracks(t, "detail.media.tracks", list1(t, dm["tracks"]))
	checkChapters(t, "detail.media.chapters", list1(t, dm["chapters"]))

	// Play (it sends no mediaPlayer), sync, close.
	play := h.jsonUA(ua, "POST", "/api/items/"+key+"/play", map[string]any{
		"deviceInfo": map[string]any{"deviceId": strings.Repeat("x", 100), "clientName": "ShelfPlayer", "clientVersion": "3.3.0",
			"manufacturer": "Apple", "model": "iPhone16,1"},
		"supportedMimeTypes": []string{"audio/flac", "audio/mpeg", "audio/mp4", "audio/aac", "audio/x-aiff"},
	})
	fieldKinds(t, "play", play, map[string]string{"id": "string", "audioTracks": "array", "chapters": "array", "startTime": "number"})
	checkTracks(t, "play.audioTracks", list1(t, play["audioTracks"]))
	checkMetadata(t, "play.mediaMetadata", obj1(t, play["mediaMetadata"]))
	sid := play["id"].(string)
	ino := obj1(t, list1(t, play["audioTracks"])[0])["ino"].(string)
	if code, _ := h.doUA(ua, "GET", "/api/items/"+key+"/file/"+ino, nil, map[string]string{"Range": "bytes=0-99"}); code != 206 {
		t.Fatalf("download/stream with the bearer header: HTTP %d", code)
	}
	for _, body := range []map[string]float64{{"duration": 36000, "currentTime": 600, "timeListened": 0}, {"duration": 36000, "currentTime": 630, "timeListened": 30}} {
		if code, out := h.doUA(ua, "POST", "/api/session/"+sid+"/sync", body, nil); code != 200 {
			t.Fatalf("sync: %d %s", code, out)
		}
	}
	if code, _ := h.doUA(ua, "POST", "/api/session/"+sid+"/close", map[string]float64{"duration": 36000, "currentTime": 640, "timeListened": 0}, nil); code != 200 {
		t.Fatal("close failed")
	}
	auth = obj1(t, h.jsonUA(ua, "POST", "/api/authorize", nil)["user"])
	mp := list1(t, auth["mediaProgress"])
	if len(mp) != 1 {
		t.Fatalf("mediaProgress after playing: %v", mp)
	}
	p := obj1(t, mp[0])
	checkProgress(t, "authorize.mediaProgress", p)
	if p["currentTime"] != 640.0 {
		t.Fatalf("place after close = %v, want 640", p["currentTime"])
	}

	// Mark finished from a fresh device (a client-made id it doesn't know), then not.
	batch := func(fin bool, cur, prog float64) {
		t.Helper()
		code, out := h.doUA(ua, "PATCH", "/api/me/progress/batch/update", []any{map[string]any{
			"id": "6B1C8E4A-1F0B-4E57-9C55-8A3E6D2F0B11", "libraryItemId": key, "duration": 0, "progress": prog,
			"currentTime": cur, "isFinished": fin, "hideFromContinueListening": false, "lastUpdate": time.Now().UnixMilli(),
			"startedAt": 0, "finishedAt": 0}}, nil)
		if code != 200 {
			t.Fatalf("batch/update: %d %s", code, out)
		}
	}
	batch(true, 0, 1)
	if got := h.jsonUA(ua, "GET", "/api/me/progress/"+key, nil); got["isFinished"] != true {
		t.Fatalf("mark finished: %v", got)
	}
	batch(false, 0, 0)
	if got := h.jsonUA(ua, "GET", "/api/me/progress/"+key, nil); got["isFinished"] != false {
		t.Fatalf("mark not finished: %v", got)
	}

	// Bookmarks: integer seconds.
	bm := h.jsonUA(ua, "POST", "/api/me/item/"+key+"/bookmark", map[string]any{"title": "Mordecai", "time": 615})
	fieldKinds(t, "bookmark", bm, map[string]string{"libraryItemId": "string", "title": "string", "time": "number", "createdAt": "number"})
	if code, _ := h.doUA(ua, "DELETE", "/api/me/item/"+key+"/bookmark/615", nil, nil); code != 200 {
		t.Fatal("bookmark delete failed")
	}

	// Offline listening: /status, /api/me, then /api/session/local with a full session.
	h.jsonUA(ua, "GET", "/status", nil)
	me := h.jsonUA(ua, "GET", "/api/me", nil)
	fieldKinds(t, "me", me, map[string]string{"id": "string", "username": "string", "type": "string", "isActive": "bool", "isLocked": "bool"})
	code, out = h.doUA(ua, "POST", "/api/session/local", map[string]any{
		"id": "2F0C55B1-7D7B-4E0F-A4A4-55C0E5E2E7A1", "userId": me["id"], "libraryId": libID, "libraryItemId": key,
		"episodeId": nil, "mediaType": "book", "mediaMetadata": obj1(t, media["metadata"]), "displayTitle": "Dungeon Crawler Carl",
		"displayAuthor": "Matt Dinniman", "coverPath": nil, "duration": 36000, "playMethod": 3, "mediaPlayer": "ShelfPlayer",
		"deviceInfo": map[string]any{"id": "x", "userId": me["id"], "deviceId": "y", "osName": "iOS", "osVersion": "18.5",
			"deviceType": "iPhone", "manufacturer": "Apple", "model": "iPhone16,1", "clientName": "ShelfPlayer", "clientVersion": "3.3.0"},
		"date": "2026-10-11", "dayOfWeek": "Sunday", "serverVersion": ServerVersion, "timeListening": 120.0,
		"startTime": 0.0, "currentTime": 900.0, "startedAt": float64(time.Now().UnixMilli()), "updatedAt": float64(time.Now().Add(time.Minute).UnixMilli()),
	}, nil)
	if code != 200 {
		t.Fatalf("offline upload: %d %s", code, out)
	}
	if got := h.jsonUA(ua, "GET", "/api/me/progress/"+key, nil)["currentTime"]; got != 900.0 {
		t.Fatalf("place after the offline upload = %v, want 900", got)
	}

	// Duplicate progress is merged by deleting the progress id it was given.
	sessions := h.jsonUA(ua, "GET", "/api/me/listening-sessions?page=0&itemsPerPage=20", nil)
	fieldKinds(t, "listening-sessions", sessions, map[string]string{"total": "int", "numPages": "int", "page": "int", "itemsPerPage": "int", "sessions": "array"})
	if code, _ := h.doUA(ua, "DELETE", "/api/me/progress/"+p["id"].(string), nil, nil); code != 200 {
		t.Fatal("progress delete by id failed")
	}
}

// ShelfPlayer's filters: the value base64 encoded and then percent-encoded twice
// ("%3D" sent as "%253D"), and a narrator filter (no narrators here: nothing matches).
func TestShelfPlayerFilters(t *testing.T) {
	h := shapeHarnessOnly(t)
	h.signIn()
	key := itemKeyFor(h.book.ID, 0)
	lib := "/api/libraries/" + libraryID + "/items?sort=media.metadata.title&desc=0&collapseseries=0&page=0&limit=100&filter="
	count := func(filter string) int {
		t.Helper()
		return len(list1(t, h.jsonUA(shelfPlayerUA, "GET", lib+filter, nil)["results"]))
	}
	if n := count("progress.aW4tcHJvZ3Jlc3M%253D"); n != 0 { // in-progress, nothing started yet
		t.Fatalf("in-progress before listening: %d", n)
	}
	h.json("PATCH", "/api/me/progress/"+key, map[string]any{"currentTime": 60, "duration": 36000})
	if n := count("progress.aW4tcHJvZ3Jlc3M%253D"); n != 1 {
		t.Fatalf("in-progress (double-encoded): %d", n)
	}
	if n := count("progress.ZmluaXNoZWQ%253D"); n != 0 {
		t.Fatalf("finished (double-encoded): %d", n)
	}
	if n := count("narrators." + url.QueryEscape("U29tZW9uZQ==")); n != 0 {
		t.Fatalf("narrator filter matched %d", n)
	}
	aid := authorID("Matt Dinniman")
	if n := count("authors." + url.QueryEscape(b64(aid))); n != 1 {
		t.Fatalf("author filter: %d", n)
	}
}

const officialUA = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Mobile/15E148"

// The official app (iOS): connect (status needs language, ping needs success), sign in
// with x-return-tokens, reconnect through /api/authorize (it signs out if the user's
// "token" equals its access token), browse, play from the native player — which on
// servers from 2.22 streams /public/session/{id}/track/{n} with no token — sync, never
// close, mark finished by PATCH, download with ?token=, and upload everything it played
// through local-all (including streamed sessions it already synced).
func TestOfficialAppConversation(t *testing.T) {
	h := shapeHarnessOnly(t)
	key := itemKeyFor(h.book.ID, 0)
	ua := officialUA

	st := h.jsonUA(ua, "GET", "/status", nil)
	for _, k := range []string{"isInit", "language"} {
		if _, ok := st[k]; !ok {
			t.Fatalf("/status lacks %q: the app says it isn't an Audiobookshelf server", k)
		}
	}
	fieldKinds(t, "status", st, map[string]string{"authMethods": "array", "serverVersion": "string", "authFormData": "object"})
	if v := st["serverVersion"].(string); !isNumericVersion(v) || !versionAtLeast(v, "2.26.0") {
		t.Fatalf("serverVersion %q: the native code reads it as numbers, and below 2.26 it skips the access tokens", v)
	}
	if ping := h.jsonUA(ua, "GET", "/ping", nil); ping["success"] != true {
		t.Fatalf("ping: %v", ping)
	}

	code, out := h.doUA(ua, "POST", "/login", map[string]string{"username": "reader", "password": "listen-pass-1"}, map[string]string{"x-return-tokens": "true"})
	if code != 200 {
		t.Fatalf("login: %d %s", code, out)
	}
	var login map[string]any
	_ = json.Unmarshal(out, &login)
	u := obj1(t, login["user"])
	fieldKinds(t, "login.user", u, map[string]string{"id": "string", "username": "string", "accessToken": "string",
		"refreshToken": "string", "librariesAccessible": "array", "mediaProgress": "array", "bookmarks": "array",
		"permissions": "object", "type": "string"})
	if obj1(t, u["permissions"])["download"] != true {
		t.Fatal("permissions.download: the Download button needs it")
	}
	ss := obj1(t, login["serverSettings"])
	fieldKinds(t, "serverSettings", ss, map[string]string{"version": "string", "language": "string", "sortingIgnorePrefix": "bool"})
	h.token = u["accessToken"].(string)
	refresh := u["refreshToken"].(string)

	auth := h.jsonUA(ua, "POST", "/api/authorize", nil)
	au := obj1(t, auth["user"])
	if au["token"] == h.token || au["isOldToken"] == true {
		t.Fatalf("authorize: the app would sign itself out (token %v, isOldToken %v)", au["token"], au["isOldToken"])
	}
	fieldKinds(t, "authorize", auth, map[string]string{"user": "object", "userDefaultLibraryId": "string", "serverSettings": "object"})

	lib := obj1(t, list1(t, h.jsonUA(ua, "GET", "/api/libraries", nil)["libraries"])[0])
	fieldKinds(t, "library", lib, map[string]string{"id": "string", "name": "string", "icon": "string", "mediaType": "string", "settings": "object"})
	fieldKinds(t, "library.settings", obj1(t, lib["settings"]), map[string]string{"coverAspectRatio": "int", "audiobooksOnly": "bool"})
	libID := lib["id"].(string)
	full := h.jsonUA(ua, "GET", "/api/libraries/"+libID+"?include=filterdata", nil)
	fieldKinds(t, "library?include=filterdata", full, map[string]string{"library": "object", "filterdata": "object", "issues": "int", "numUserPlaylists": "int"})
	fieldKinds(t, "filterdata", obj1(t, full["filterdata"]), map[string]string{"authors": "array", "series": "array", "genres": "array",
		"tags": "array", "narrators": "array", "languages": "array"})
	code, out = h.doUA(ua, "GET", "/api/libraries/"+libID+"/personalized?minified=1&include=rssfeed,numEpisodesIncomplete", nil, nil)
	var shelves []map[string]any
	if code != 200 || json.Unmarshal(out, &shelves) != nil || len(shelves) == 0 {
		t.Fatalf("personalized must be a top-level list: %d %s", code, out)
	}
	h.jsonUA(ua, "GET", "/api/libraries/"+libID+"/items?sort=media.metadata.title&desc=0&limit=50&page=0&minified=1&include=rssfeed,numEpisodesIncomplete", nil)

	page := h.jsonUA(ua, "GET", "/api/items/"+key+"?expanded=1&include=rssfeed", nil)
	fieldKinds(t, "item page", page, map[string]string{"libraryId": "string", "mediaType": "string", "isMissing": "bool", "isInvalid": "bool"})
	if len(list1(t, obj1(t, page["media"])["tracks"])) == 0 {
		t.Fatal("no media.tracks: the Play button stays hidden")
	}

	// Native play: string flags, no supportedMimeTypes.
	play := h.jsonUA(ua, "POST", "/api/items/"+key+"/play", map[string]any{
		"forceDirectPlay": "1", "forceTranscode": "", "mediaPlayer": "AVPlayer",
		"deviceInfo": map[string]any{"deviceId": "5B0E2F5C-1D8A-4C0E-9C2B-3B8E1A7F6D40", "manufacturer": "Apple", "model": "iPhone15,2", "clientVersion": "0.14.2"},
	})
	fieldKinds(t, "PlaybackSession", play, map[string]string{"id": "string", "mediaType": "string", "duration": "number", "playMethod": "int",
		"timeListening": "number", "currentTime": "number", "libraryItemId": "string", "startedAt": "number", "updatedAt": "number",
		"userId": "string?", "episodeId": "string?", "displayTitle": "string?", "displayAuthor": "string?", "coverPath": "string?"})
	checkMetadata(t, "PlaybackSession.mediaMetadata", obj1(t, play["mediaMetadata"]))
	checkChapters(t, "PlaybackSession.chapters", list1(t, play["chapters"]))
	tracks := list1(t, play["audioTracks"])
	checkTracks(t, "PlaybackSession.audioTracks", tracks)
	li := obj1(t, play["libraryItem"])
	fieldKinds(t, "PlaybackSession.libraryItem", li, map[string]string{"id": "string", "ino": "string", "libraryId": "string",
		"folderId": "string", "path": "string", "relPath": "string", "isFile": "bool", "mtimeMs": "int", "ctimeMs": "int",
		"birthtimeMs": "int", "addedAt": "int", "updatedAt": "int", "isMissing": "bool", "isInvalid": "bool", "mediaType": "string"})
	for i, af := range list1(t, obj1(t, li["media"])["audioFiles"]) {
		fieldKinds(t, fmt.Sprintf("libraryItem.media.audioFiles[%d]", i), obj1(t, af), map[string]string{"ino": "string", "metadata": "object"})
	}
	if play["playMethod"] != 0.0 {
		t.Fatalf("playMethod %v: direct play is what the stream link below serves", play["playMethod"])
	}
	sid := play["id"].(string)

	// The stream link: no token at all, Range for seeking.
	idx := int(obj1(t, tracks[0])["index"].(float64))
	streamPath := fmt.Sprintf("/public/session/%s/track/%d", sid, idx)
	saved := h.token
	h.token = ""
	if code, _ := h.doUA("AppleCoreMedia/1.0.0.22F76 (iPhone; U; CPU OS 18_5 like Mac OS X; en_us)", "GET", streamPath, nil,
		map[string]string{"Range": "bytes=0-99"}); code != 206 {
		t.Fatalf("stream by session: HTTP %d, want 206", code)
	}
	for _, bad := range []string{
		fmt.Sprintf("/public/session/%s/track/99", sid),                                   // no such track
		"/public/session/00000000-0000-4000-8000-000000000000/track/1",                    // no such session
		fmt.Sprintf("/public/session/%s/track/x", sid),                                    // not a number
		fmt.Sprintf("/public/session/%s/track/%d", strings.ToUpper(sid[:8])+sid[8:], idx), // ids are exact
	} {
		if code, _ := h.do("GET", bad, nil, nil); code == 200 || code == 206 {
			t.Errorf("%s: HTTP %d", bad, code)
		}
	}
	h.token = saved

	// Sync every 15 s; it never closes.
	if code, _ := h.doUA(ua, "POST", "/api/session/"+sid+"/sync", map[string]float64{"currentTime": 420, "duration": 36000, "timeListened": 15}, nil); code != 200 {
		t.Fatal("sync failed")
	}
	p := h.jsonUA(ua, "GET", "/api/me/progress/"+key, nil)
	checkProgress(t, "GET progress", p)
	if p["libraryItemId"] != key || p["currentTime"] != 420.0 {
		t.Fatalf("progress after sync: %v", p)
	}

	// Download: the full item with progress, then files and cover by ?token=.
	dl := h.jsonUA(ua, "GET", "/api/items/"+key+"?expanded=1&include=progress", nil)
	checkProgress(t, "download userMediaProgress", obj1(t, dl["userMediaProgress"]))
	for _, tr := range list1(t, obj1(t, dl["media"])["tracks"]) {
		ino := obj1(t, tr)["ino"].(string)
		h.token = ""
		code, _ := h.doUA(ua, "GET", "/api/items/"+key+"/file/"+ino+"/download?token="+url.QueryEscape(saved), nil, nil)
		h.token = saved
		if code != 200 {
			t.Fatalf("download with ?token=: HTTP %d", code)
		}
	}

	// Offline: a downloaded-book session reported as it plays, then the start-up upload
	// of every session — the streamed one included, carrying only the unsynced remainder.
	local := map[string]any{"id": "E6C0F7B2-77A4-4C1F-9C47-0F3E0D59A6D2", "libraryItemId": key, "mediaType": "book",
		"displayTitle": "Dungeon Crawler Carl", "displayAuthor": "Matt Dinniman", "duration": 36000.0, "playMethod": 3,
		"startedAt": float64(time.Now().UnixMilli()), "updatedAt": float64(time.Now().Add(time.Minute).UnixMilli()), "timeListening": 300.0, "currentTime": 720.0,
		"mediaPlayer": "AVPlayer", "deviceInfo": map[string]any{"deviceId": "5B0E2F5C", "manufacturer": "Apple", "model": "iPhone15,2", "clientVersion": "0.14.2"}}
	if code, out := h.doUA(ua, "POST", "/api/session/local", local, nil); code != 200 {
		t.Fatalf("session/local: %d %s", code, out)
	}
	streamed := map[string]any{"id": sid, "libraryItemId": key, "mediaType": "book", "duration": 36000.0, "playMethod": 0,
		"timeListening": 5.0, "currentTime": 425.0, "startedAt": float64(time.Now().Add(-20 * time.Minute).UnixMilli()), "updatedAt": float64(time.Now().Add(-10 * time.Minute).UnixMilli()), "mediaPlayer": "AVPlayer"}
	all := h.jsonUA(ua, "POST", "/api/session/local-all", map[string]any{"sessions": []any{local, streamed},
		"deviceInfo": map[string]any{"deviceId": "5B0E2F5C", "manufacturer": "Apple", "model": "iPhone15,2", "clientVersion": "0.14.2"}})
	for _, r := range list1(t, all["results"]) {
		if obj1(t, r)["success"] != true {
			t.Fatalf("local-all: %v", all)
		}
	}
	me := h.jsonUA(ua, "GET", "/api/me", nil)
	fieldKinds(t, "me", me, map[string]string{"id": "string", "username": "string", "mediaProgress": "array"})
	got := obj1(t, list1(t, me["mediaProgress"])[0])
	checkProgress(t, "me.mediaProgress", got)
	if got["currentTime"] != 720.0 {
		t.Fatalf("place after the uploads = %v, want 720 (the downloaded session is newest; the old streamed one mustn't win)", got["currentTime"])
	}

	// Mark finished from the item menu.
	if code, _ := h.doUA(ua, "PATCH", "/api/me/progress/"+key, map[string]any{"isFinished": true}, nil); code != 200 {
		t.Fatal("mark finished failed")
	}
	if h.jsonUA(ua, "GET", "/api/me/progress/"+key, nil)["isFinished"] != true {
		t.Fatal("not finished")
	}

	stats := h.jsonUA(ua, "GET", "/api/me/listening-stats", nil)
	fieldKinds(t, "listening-stats", stats, map[string]string{"totalTime": "number", "days": "object", "recentSessions": "array"})

	// A 401 sends it to /auth/refresh with x-refresh-token and an empty JSON body.
	code, out = h.doUA(ua, "POST", "/auth/refresh", map[string]any{}, map[string]string{"x-refresh-token": refresh})
	var ref map[string]any
	if code != 200 || json.Unmarshal(out, &ref) != nil {
		t.Fatalf("refresh: %d %s", code, out)
	}
	fieldKinds(t, "refresh.user", obj1(t, ref["user"]), map[string]string{"accessToken": "string"})
}

// Once a session is closed, or its owner loses access, its stream link stops working.
func TestSessionStreamLinkEnds(t *testing.T) {
	h := shapeHarnessOnly(t)
	h.signIn()
	key := itemKeyFor(h.book.ID, 0)
	sid := h.play(key)
	link := "/public/session/" + sid + "/track/1"
	if code, _ := h.do("GET", link, nil, map[string]string{"Authorization": ""}); code != 200 {
		t.Fatalf("open session: HTTP %d", code)
	}
	if code, _ := h.do("POST", "/api/session/"+sid+"/close", nil, nil); code != 200 {
		t.Fatal("close failed")
	}
	if code, _ := h.do("GET", link, nil, nil); code != http.StatusNotFound {
		t.Fatalf("closed session still streams: HTTP %d", code)
	}

	sid = h.play(key)
	link = "/public/session/" + sid + "/track/1"
	_ = h.srv.SetAllowed(t.Context(), h.readerID(), false)
	if code, _ := h.do("GET", link, nil, nil); code != http.StatusNotFound {
		t.Fatalf("a switched-off user's session still streams: HTTP %d", code)
	}
}

// shapeHarnessOnly is the shape test's harness (a book with a series, a year and a cover).
func shapeHarnessOnly(t *testing.T) *harness {
	t.Helper()
	h, _ := shapeHarness(t)
	return h
}

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

func isNumericVersion(v string) bool {
	_, ok := parseVersion(v)
	return ok
}

func versionAtLeast(v, min string) bool {
	a, _ := parseVersion(v)
	b, _ := parseVersion(min)
	return !versionLess(a, b)
}
