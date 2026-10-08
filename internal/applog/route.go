package applog

import (
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strings"
)

// Request log lines say which kind of call was made, never which thing it was about.
// A path like /api/items/b45/play names the book someone pressed play on; next to the
// "signed in" line, or the admin's Listening tab, that's who listened to what — exactly
// what the audiobook mandate rules out (admins see how much and when, never what). So
// request logging records the route pattern ("POST /api/items/{id}/play") and the names
// of the query parameters, not their values.

var idSegment = []*regexp.Regexp{
	regexp.MustCompile(`^m?b\d+(v\d+)?$`),                                                    // audiobook item / media keys: b12, b12v3, mb12
	regexp.MustCompile(`^(?i)[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`), // UUIDs (sessions, devices)
	regexp.MustCompile(`^(au|se)[0-9a-f]{12}$`),                                              // author and series ids
	regexp.MustCompile(`^u\d+$`),                                                             // user ids
	regexp.MustCompile(`^(?i)[0-9a-f]{12,}$`),                                                // long hex: hashes, tokens
	regexp.MustCompile(`\d`),                                                                 // anything else carrying a number
}

// versionSegment is the one numbered segment worth keeping: /api/v1.
var versionSegment = regexp.MustCompile(`^v\d+$`)

// RedactPath replaces every path segment that looks like an identifier with {id}, for
// paths no route pattern matched. It errs towards hiding: losing a little detail in a
// 404 line is fine, naming a book is not.
func RedactPath(p string) string {
	if p == "" {
		return p
	}
	segs := strings.Split(p, "/")
	for i, s := range segs {
		if s == "" || versionSegment.MatchString(s) {
			continue
		}
		for _, re := range idSegment {
			if re.MatchString(s) {
				segs[i] = "{id}"
				break
			}
		}
	}
	return strings.Join(segs, "/")
}

// RouteLabel is what a request log line calls the request: the matched route pattern
// when there is one (Go's ServeMux fills r.Pattern in place, so it's visible after the
// handler has run), otherwise the method and the redacted path. A subtree pattern such
// as the catch-all "/" says nothing useful, so it falls back too.
func RouteLabel(r *http.Request) string {
	if p := r.Pattern; p != "" && !strings.HasSuffix(p, "/") {
		if !strings.Contains(p, " ") {
			p = r.Method + " " + p
		}
		return p
	}
	return r.Method + " " + RedactPath(r.URL.Path)
}

// QueryKeys returns the sorted, comma-joined names of the query parameters — never
// their values, which carry search terms — minus any in drop.
func QueryKeys(q url.Values, drop ...string) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		skip := false
		for _, d := range drop {
			if k == d {
				skip = true
				break
			}
		}
		if !skip {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
