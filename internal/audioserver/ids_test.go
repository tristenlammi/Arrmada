package audioserver

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

// Every id's UUID form turns back into the same thing, without a lookup table; the parser
// refuses anything that isn't exactly one of ours.
func TestUUIDCodecRoundTrip(t *testing.T) {
	var c uuidCodec
	for _, k := range []struct{ book, version int64 }{
		{1, 0}, {12, 3}, {maxOwnerID, 0}, {maxOwnerID, maxVersionID}, {7, 1 << 39}, {123456789, 987654321},
	} {
		key := itemKeyFor(k.book, k.version)
		for _, id := range []string{c.item(key), c.media(key)} {
			if !uuidShape.MatchString(id) || id[14] != '4' || !strings.ContainsRune("89ab", rune(id[19])) {
				t.Errorf("%s: %q isn't a version-4 UUID", key, id)
			}
			if got, ok := itemKeyFromID(id); !ok || got != key {
				t.Errorf("%s → %s → %q (%v)", key, id, got, ok)
			}
			if got, ok := itemKeyFromID(strings.ToUpper(id)); !ok || got != key {
				t.Errorf("upper-case %s → %q (%v)", id, got, ok)
			}
		}
		if c.item(key) == c.media(key) {
			t.Errorf("%s: item and media ids collide", key)
		}
	}
	// Legacy ids, and their media ids, read as themselves.
	for in, want := range map[string]string{"b12": "b12", "b12v3": "b12v3", "mb12v3": "b12v3", "mb7": "b7"} {
		if got, ok := itemKeyFromID(in); !ok || got != want {
			t.Errorf("%s → %q (%v), want %s", in, got, ok, want)
		}
	}
	if u := c.user(42); !uuidShape.MatchString(u) {
		t.Errorf("user id %q", u)
	} else if b, v, ok := unpackUUID(markUser, u); !ok || b != 42 || v != 0 {
		t.Errorf("user id %q unpacks to %d %d %v", u, b, v, ok)
	}

	good := c.item("b12v3")
	for _, bad := range []string{
		"", "b", "b0", "bx", "v3", "arrmada-audiobooks", "au0123456789ab",
		markUser + good[8:],                     // a user id is not an item
		libraryUUID,                             // nor is the library
		good[:14] + "5" + good[15:],             // version nibble
		good[:19] + "c" + good[20:],             // variant bits
		good[:19] + "9" + good[20:],             // the spare bits after the variant
		good[:8] + "_" + good[9:],               // separators
		good + "0",                              // length
		good[:24] + "000000000000",              // book 0
		strings.Replace(good, "-", "", 1) + "0", // shifted separator
		good[:9] + "zzzz" + good[13:],           // not hex
		"00000000-0000-4000-8000-000000000000",  // a random UUID (a session id)
		nameUUID("author", "Matt Dinniman"),     // an author
	} {
		if k, ok := itemKeyFromID(bad); ok {
			t.Errorf("%q was taken as item %s", bad, k)
		}
	}
	if !isLibraryID(libraryID) || !isLibraryID(libraryUUID) || isLibraryID(good) {
		t.Error("library ids")
	}
	if !isAuthorID(nameUUID("author", "Matt Dinniman"), " matt dinniman ") || isAuthorID(nameUUID("series", "Matt Dinniman"), "Matt Dinniman") {
		t.Error("author ids")
	}
	var l legacyCodec
	if l.item("b12v3") != "b12v3" || l.media("b12v3") != "mb12v3" || l.user(3) != "u3" || l.library() != libraryID ||
		l.author("Matt Dinniman") != authorID("Matt Dinniman") || l.series("The Crawl") != seriesID("The Crawl") {
		t.Error("the legacy codec must reproduce today's ids exactly")
	}
	if c.library() != libraryUUID || c.author("X") != nameUUID("author", "X") || c.series("X") != nameUUID("series", "X") {
		t.Error("uuid codec library/author/series")
	}
}

// Every route that takes an item, library, author or series id answers the same for the
// classic id and its UUID form, and a place set through either lands in the same place.
func TestRoutesAcceptBothIdShapes(t *testing.T) {
	h := shapeHarnessOnly(t)
	h.signIn()
	var c uuidCodec
	key := itemKeyFor(h.book.ID, 0)
	ids := map[string][2]string{
		"item":    {key, c.item(key)},
		"lib":     {libraryID, libraryUUID},
		"author":  {authorID("Matt Dinniman"), c.author("Matt Dinniman")},
		"series":  {seriesID("The Crawl"), c.series("The Crawl")},
		"mediaID": {key, c.media(key)},
	}
	// Reads answer byte for byte the same (once the moving parts are pinned).
	for _, p := range []string{
		"/api/items/{item}?expanded=1",
		"/api/libraries/{lib}",
		"/api/libraries/{lib}/items?limit=10&page=0",
		"/api/libraries/{lib}/items?filter=authors.{author64}",
		"/api/libraries/{lib}/items?filter=series.{series64}",
		"/api/authors/{author}?include=items",
		"/api/series/{series}",
		"/api/libraries/{lib}/series/{series}",
		"/api/me/bookmarks/{item}",
	} {
		var outs [2]string
		for i := 0; i < 2; i++ {
			path := strings.NewReplacer("{item}", ids["item"][i], "{lib}", ids["lib"][i], "{author}", ids["author"][i],
				"{series}", ids["series"][i], "{author64}", url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(ids["author"][i]))),
				"{series64}", url.QueryEscape(base64.StdEncoding.EncodeToString([]byte(ids["series"][i])))).Replace(p)
			code, out := h.do("GET", path, nil, nil)
			if code != 200 {
				t.Fatalf("%s: HTTP %d %s", path, code, out)
			}
			outs[i] = stripVolatile(string(out))
		}
		if outs[0] != outs[1] {
			t.Errorf("%s answers differently for the UUID form:\n%s\n%s", p, outs[0], outs[1])
		}
		if strings.Contains(outs[1], "a7d10b") {
			t.Errorf("%s: a UUID id leaked into the reply: %s", p, outs[1])
		}
	}
	if code, _ := h.do("GET", "/api/libraries/"+c.item(key)+"/items", nil, nil); code != 404 {
		t.Errorf("an item UUID taken as the library: HTTP %d", code)
	}

	// Writes through the UUID form reach the classic place.
	uid := ids["item"][1]
	h.json("PATCH", "/api/me/progress/"+uid, map[string]any{"currentTime": 100, "duration": 36000})
	if p := h.json("GET", "/api/me/progress/"+key, nil); p["currentTime"] != 100.0 || p["id"] != key {
		t.Fatalf("PATCH by UUID: %v", p)
	}
	sid := h.json("POST", "/api/items/"+uid+"/play", map[string]any{"deviceInfo": map[string]string{"deviceId": "d"}})
	if sid["libraryItemId"] != key {
		t.Fatalf("play by UUID: libraryItemId %v", sid["libraryItemId"])
	}
	if code, _ := h.do("POST", "/api/session/"+sid["id"].(string)+"/sync", map[string]float64{"currentTime": 200, "timeListened": 5}, nil); code != 200 {
		t.Fatal("sync")
	}
	if p := h.json("GET", "/api/me/progress/"+uid, nil); p["currentTime"] != 200.0 {
		t.Fatalf("GET progress by UUID after sync: %v", p)
	}
	res := list1(t, h.json("POST", "/api/session/local-all", map[string]any{"sessions": []any{map[string]any{
		"id": "off-uuid", "libraryItemId": ids["mediaID"][1], "currentTime": 300, "duration": 36000, "timeListening": 60,
		"startedAt": 1, "updatedAt": 9_999_999_999_999}}})["results"])
	if obj1(t, res[0])["success"] != true {
		t.Fatalf("local-all by media UUID: %v", res)
	}
	h.do("PATCH", "/api/me/progress/batch/update", []any{map[string]any{"libraryItemId": uid, "isFinished": true}}, nil)
	all := list1(t, h.json("GET", "/api/me/progress", nil)["mediaProgress"])
	if len(all) != 1 || obj1(t, all[0])["id"] != key || obj1(t, all[0])["isFinished"] != true {
		t.Fatalf("one classic place, finished: %v", all)
	}
	h.json("POST", "/api/me/item/"+uid+"/bookmark", map[string]any{"time": 61, "title": "via uuid"})
	if bm := list1(t, h.json("GET", "/api/me/bookmarks/"+key, nil)["bookmarks"]); len(bm) != 1 || obj1(t, bm[0])["libraryItemId"] != key {
		t.Fatalf("bookmark by UUID: %v", bm)
	}
	if code, _ := h.do("DELETE", "/api/me/item/"+uid+"/bookmark/61", nil, nil); code != 200 {
		t.Fatal("bookmark delete by UUID")
	}
	if bm := list1(t, h.json("GET", "/api/me/bookmarks", nil)["bookmarks"]); len(bm) != 0 {
		t.Fatalf("bookmark survived delete by UUID: %v", bm)
	}
	batch := list1(t, h.json("POST", "/api/items/batch/get", map[string]any{"libraryItemIds": []string{uid}})["libraryItems"])
	if len(batch) != 1 || obj1(t, batch[0])["id"] != key {
		t.Fatalf("batch/get by UUID: %v", batch)
	}
}

// stripVolatile drops what changes between two identical requests (clock-stamped fields)
// and filterBy, which echoes the filter exactly as the app sent it, as Audiobookshelf does.
func stripVolatile(s string) string {
	for _, k := range []string{`"lastScan":`, `"lastSeen":`, `"filterBy":`} {
		for {
			i := strings.Index(s, k)
			if i < 0 {
				break
			}
			j := i + len(k)
			for j < len(s) && s[j] != ',' && s[j] != '}' {
				j++
			}
			s = s[:i] + "#" + s[j:]
		}
	}
	return s
}
