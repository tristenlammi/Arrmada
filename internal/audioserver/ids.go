package audioserver

import (
	"crypto/sha1"
	"fmt"
	"net/http"
	"strconv"
	"strings"
)

// Ids as apps see them. Arrmada's own ids are short ("arrmada-audiobooks", "b12v3",
// "u3", "au0123456789ab"); Audiobookshelf's are UUIDs, and some apps may only cope with
// those. Every id has a UUID form that turns back into the same thing without a lookup
// table, so the server can take either shape on every incoming route. Replies still use
// the classic ids — the ids an app already has (and the downloads filed under them)
// never change. Inside Arrmada, places, bookmarks and sessions always use the classic
// item key; nothing is ever stored in UUID form.
//
// The UUID form of an item, media or user id:
//
//	KKKKKKKK-VVVV-4VVV-8VVV-NNNNNNNNNNNN
//
// K is a fixed marker saying which kind of id it is, V the audiobook version id (40 bits,
// 0 for a book's standard audiobook) with the UUID version nibble '4' and the variant
// bits '10' in their usual places (the two bits after the variant are always 0), and N
// the book or user id (48 bits). Author and series ids are a name-based UUID (version 5)
// made from the same lower-cased name the classic id hashes.

// idCodec turns Arrmada's ids into the shape a device sees.
type idCodec interface {
	item(key string) string
	media(key string) string
	user(id int64) string
	library() string
	author(name string) string
	series(name string) string
}

// legacyCodec is today's ids, exactly.
type legacyCodec struct{}

func (legacyCodec) item(key string) string    { return key }
func (legacyCodec) media(key string) string   { return "m" + key }
func (legacyCodec) user(id int64) string      { return "u" + itoa(id) }
func (legacyCodec) library() string           { return libraryID }
func (legacyCodec) author(name string) string { return authorID(name) }
func (legacyCodec) series(name string) string { return seriesID(name) }

// uuidCodec is the reversible UUID form (see the layout above).
type uuidCodec struct{}

// Kind markers, the UUID's first group. Any fixed values would do; these just have to
// differ from each other.
const (
	markItem  = "a7d10b00"
	markMedia = "a7d10b01"
	markUser  = "a7d10b02"
)

// libraryUUID is the one library's UUID form.
const libraryUUID = "a7d10b03-0000-4000-8000-000000000001"

const (
	maxVersionID = 1<<40 - 1
	maxOwnerID   = 1<<48 - 1
)

func (uuidCodec) item(key string) string {
	b, v, ok := parseLegacyItemKey(key)
	if !ok {
		return key
	}
	return packUUID(markItem, b, v)
}

func (uuidCodec) media(key string) string {
	b, v, ok := parseLegacyItemKey(key)
	if !ok {
		return "m" + key
	}
	return packUUID(markMedia, b, v)
}

func (uuidCodec) user(id int64) string      { return packUUID(markUser, id, 0) }
func (uuidCodec) library() string           { return libraryUUID }
func (uuidCodec) author(name string) string { return nameUUID("author", name) }
func (uuidCodec) series(name string) string { return nameUUID("series", name) }

// packUUID lays an owner id (book or user) and a version id out as described above. Ids
// out of range fall back to 0 rather than wrapping into someone else's id.
func packUUID(mark string, owner, version int64) string {
	if owner < 0 || owner > maxOwnerID {
		owner = 0
	}
	if version < 0 || version > maxVersionID {
		version = 0
	}
	v := uint64(version)
	g2 := (v >> 24) & 0xffff
	g3 := 0x4000 | (v>>12)&0x0fff
	g4 := 0x8000 | v&0x0fff
	return fmt.Sprintf("%s-%04x-%04x-%04x-%012x", mark, g2, g3, g4, uint64(owner))
}

// unpackUUID reads an id packUUID made with the given marker (lower case; callers fold
// case first). It's strict: wrong marker, wrong version or variant nibble, the spare bits
// set or anything malformed is not an id of ours.
func unpackUUID(mark, s string) (owner, version int64, ok bool) {
	if len(s) != 36 || s[8] != '-' || s[13] != '-' || s[18] != '-' || s[23] != '-' || s[:8] != mark {
		return 0, 0, false
	}
	hexPart := func(p string) (uint64, bool) {
		for _, c := range p {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
				return 0, false
			}
		}
		n, err := strconv.ParseUint(p, 16, 64)
		return n, err == nil
	}
	g2, ok2 := hexPart(s[9:13])
	g3, ok3 := hexPart(s[14:18])
	g4, ok4 := hexPart(s[19:23])
	g5, ok5 := hexPart(s[24:36])
	if !(ok2 && ok3 && ok4 && ok5) || g3&0xf000 != 0x4000 || g4&0xf000 != 0x8000 {
		return 0, 0, false
	}
	v := g2<<24 | (g3&0x0fff)<<12 | g4&0x0fff
	return int64(g5), int64(v), true
}

// nameUUID is a version-5-style UUID from a name, the way the classic author and series
// ids are a hash of it (kind keeps an author and a series of the same name apart).
func nameUUID(kind, name string) string {
	h := sha1.Sum([]byte(kind + ":" + strings.ToLower(strings.TrimSpace(name))))
	h[6] = h[6]&0x0f | 0x50
	h[8] = h[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", h[0:4], h[4:6], h[6:8], h[8:10], h[10:16])
}

// --- incoming ids ------------------------------------------------------------

// itemKeyFromID turns any item id an app may send — the classic "b12"/"b12v3", its
// media id "mb12v3", or either one's UUID form — into the internal item key. ok is false
// when it isn't an item id of either shape.
func itemKeyFromID(id string) (string, bool) {
	if b, v, ok := parseLegacyItemKey(id); ok {
		return itemKeyFor(b, v), true
	}
	if strings.HasPrefix(id, "m") {
		if b, v, ok := parseLegacyItemKey(id[1:]); ok {
			return itemKeyFor(b, v), true
		}
	}
	lower := strings.ToLower(id)
	for _, mark := range []string{markItem, markMedia} {
		if b, v, ok := unpackUUID(mark, lower); ok && b > 0 {
			return itemKeyFor(b, v), true
		}
	}
	return "", false
}

// normalItemID is itemKeyFromID, passing anything that isn't an item id through
// unchanged (so an unknown id still answers "not found" exactly as before).
func normalItemID(id string) string {
	if k, ok := itemKeyFromID(id); ok {
		return k
	}
	return id
}

// isLibraryID accepts the library's classic id or its UUID.
func isLibraryID(id string) bool { return id == libraryID || strings.EqualFold(id, libraryUUID) }

// isAuthorID reports whether id names the author called name, in either shape.
func isAuthorID(id, name string) bool {
	return id == authorID(name) || strings.EqualFold(id, nameUUID("author", name))
}

// isSeriesID reports whether id names the series called name, in either shape.
func isSeriesID(id, name string) bool {
	return id == seriesID(name) || strings.EqualFold(id, nameUUID("series", name))
}

// normalizePathIDs rewrites the item and library ids in a matched route's path to their
// classic form, so every handler below works with internal keys whichever shape the app
// sent. Session ids ({sid} under /api/session/) are already UUIDs and are left alone, as
// are author and series ids, which handlers match by name in both shapes.
func normalizePathIDs(r *http.Request) {
	if id := r.PathValue("id"); id != "" {
		r.SetPathValue("id", normalItemID(id))
	}
	if lib := r.PathValue("lib"); strings.EqualFold(lib, libraryUUID) {
		r.SetPathValue("lib", libraryID)
	}
}
