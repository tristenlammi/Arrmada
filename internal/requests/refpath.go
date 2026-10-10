package requests

import (
	"net/url"
	"regexp"
	"strings"
)

// bookRefSuffix is what a book reference carries after the book's key: a decision
// (":approved:<unix>", ":declined:<unix>") and/or the format that arrived
// (":audiobook"). The key itself can hold a ':' (Hardcover's "hc:123"), so these come
// off the end rather than splitting on ':'.
var bookRefSuffix = regexp.MustCompile(`(:(approved|declined):\d+|:(audiobook|ebook|ready))$`)

// RefPath is the app address an inbox reference leads to, for a Web Push tap to open:
// the exact title ("movie:603…" → /discover/movie/603, "series:1399…" →
// /discover/series/1399), the book on Discover's Books tab ("book:<key>…" →
// /discover?tab=books&work=<key>), or the request ("request:40…" → /requests?id=40).
// "" for a reference it doesn't know; the caller falls back to /discover.
//
// Twin: refToPath in web/src/lib/refLink.ts (the bell). Keep the two, and the test
// vectors in refpath_test.go and refLink.test.ts, in step.
func RefPath(ref string) string {
	kind, rest, ok := strings.Cut(ref, ":")
	if !ok {
		return ""
	}
	switch kind {
	case "movie", "series":
		id, _, _ := strings.Cut(rest, ":")
		if positiveID(id) {
			return "/discover/" + kind + "/" + id
		}
	case "book":
		key := rest
		for range 2 {
			key = bookRefSuffix.ReplaceAllString(key, "")
		}
		if key != "" {
			// %20 for a space, as the browser's encodeURIComponent writes it.
			return "/discover?tab=books&work=" + strings.ReplaceAll(url.QueryEscape(key), "+", "%20")
		}
	case "request":
		id, _, _ := strings.Cut(rest, ":")
		if positiveID(id) {
			return "/requests?id=" + id
		}
	}
	return ""
}

// positiveID: plain digits, no sign and no leading zero ("603", not "+603" or "0603").
func positiveID(s string) bool {
	if s == "" || s[0] == '0' {
		return false
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

// pushPath is where a Web Push for ref opens: its own address, else Discover.
func pushPath(ref string) string {
	if p := RefPath(ref); p != "" {
		return p
	}
	return "/discover"
}
