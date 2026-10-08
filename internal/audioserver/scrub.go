package audioserver

import (
	"net/url"
	"regexp"
	"sort"
	"strings"

	"github.com/tristenlammi/arrmada/internal/applog"
)

// Before request logging switched to route patterns, every audiobook-app request was
// logged with its raw path and query: which book was opened, played or bookmarked, which
// author was browsed, what was searched for. Those lines are still in the persisted log
// files (and so on the Logs page after a restart, and in any log attached to a bug
// report). ScrubLegacyEntry rewrites them to the new shape; main runs it over the files
// once, at boot, before the log is restored.

var (
	userAttr = regexp.MustCompile(`(^| )user=\S*`)
	itemAttr = regexp.MustCompile(`(^| )item=\S*`)
)

// ScrubLegacyEntry rewrites one persisted log entry so it names no book, author, series
// or search term. Entries it doesn't recognise come back unchanged. It never drops an
// entry: the line still says a request happened, when, and how it was answered.
func ScrubLegacyEntry(e applog.Entry) (applog.Entry, bool) {
	switch e.Message {
	case "audiobook server: request":
		e.Attrs = scrubRequestAttrs(e.Attrs)
	case "audiobook server: holding a jump back until playback continues from it":
		// A display name can hold a space ("John Smith"), so the next key bounds the
		// user, not whitespace; the regexes then catch whatever shape is left.
		if s, end, ok := attrSpan(e.Attrs, "user", "item"); ok {
			e.Attrs = e.Attrs[:s-len("user=")] + e.Attrs[end+1:]
		}
		e.Attrs = strings.TrimSpace(itemAttr.ReplaceAllString(userAttr.ReplaceAllString(e.Attrs, ""), ""))
	case "audiobook server: panic":
		e.Attrs = pathToRoute(e.Attrs, "err")
	// Two lines the audiobook server wrote until early October 2026, both with the raw
	// path (so the item key) of the request.
	case "audiobook server: refused a request":
		e.Attrs = pathToRoute(e.Attrs, "token")
	case "audiobook server: unsupported request":
		e.Attrs = pathToRoute(e.Attrs, "client")
	case "request": // the main API's per-request debug line
		e.Attrs = redactAttr(e.Attrs, "path", "status")
	case "panic recovered":
		e.Attrs = redactAttr(e.Attrs, "path", "")
	}
	return e, true
}

// scrubRequestAttrs turns the old "method=M path=P query=Q status=…" into the current
// "route=M P' query_keys=K status=…", with ids in P replaced and only the names of the
// query parameters kept.
func scrubRequestAttrs(attrs string) string {
	ps, pe, okPath := attrSpan(attrs, "path", "query")
	qs, qe, okQuery := attrSpan(attrs, "query", "status")
	if !okPath || !okQuery {
		// Not the shape we wrote; at least make sure no path or query value survives.
		attrs = redactAttr(attrs, "path", "status")
		if qs, qe, ok := attrSpan(attrs, "query", "status"); ok {
			attrs = attrs[:qs] + attrs[qe:]
		}
		return attrs
	}
	method := ""
	if ms, me, ok := attrSpan(attrs, "method", "path"); ok {
		method = strings.TrimSpace(attrs[ms:me])
	}
	route := strings.TrimSpace(method + " " + applog.RedactPath(strings.TrimSpace(attrs[ps:pe])))
	keys := legacyQueryKeys(strings.TrimSpace(attrs[qs:qe]))
	start := strings.Index(attrs, "method=")
	if start < 0 || start > ps {
		start = ps - len("path=")
	}
	return attrs[:start] + "route=" + route + " query_keys=" + keys + attrs[qe:]
}

// pathToRoute turns "[method=M ]path=P" (P bounded by the next key) into the current
// "route=[M ]P'", with ids in P replaced. When the next key isn't there it still
// redacts everything after "path=".
func pathToRoute(attrs, next string) string {
	ps, pe, ok := attrSpan(attrs, "path", next)
	if !ok {
		return redactAttr(attrs, "path", "")
	}
	start, method := ps-len("path="), ""
	if ms, me, ok := attrSpan(attrs, "method", "path"); ok && me <= ps {
		start, method = ms-len("method="), strings.TrimSpace(attrs[ms:me])
	}
	route := strings.TrimSpace(method + " " + applog.RedactPath(strings.TrimSpace(attrs[ps:pe])))
	return attrs[:start] + "route=" + route + attrs[pe:]
}

// redactAttr replaces the value of key (bounded by the next key, or the end) with its
// redacted form.
func redactAttr(attrs, key, next string) string {
	s, e, ok := attrSpan(attrs, key, next)
	if !ok {
		return attrs
	}
	return attrs[:s] + applog.RedactPath(strings.TrimSpace(attrs[s:e])) + attrs[e:]
}

// attrSpan finds key's value in a flattened "k=v k2=v2" attribute string: from just
// after "key=" up to " next=" (or the end when next is ""). Values can contain spaces —
// a client's user agent — so the following key, not whitespace, bounds them.
func attrSpan(attrs, key, next string) (start, end int, ok bool) {
	i := -1
	if strings.HasPrefix(attrs, key+"=") {
		i = 0
	} else if j := strings.Index(attrs, " "+key+"="); j >= 0 {
		i = j + 1
	}
	if i < 0 {
		return 0, 0, false
	}
	start = i + len(key) + 1
	if next == "" {
		return start, len(attrs), true
	}
	j := strings.Index(attrs[start:], " "+next+"=")
	if j < 0 {
		return 0, 0, false
	}
	return start, start + j, true
}

// legacyQueryKeys returns the sorted parameter names of an encoded query string.
func legacyQueryKeys(raw string) string {
	if raw == "" {
		return ""
	}
	if q, err := url.ParseQuery(raw); err == nil {
		return applog.QueryKeys(q, "token")
	}
	// Unparseable: take each name by hand, still never a value.
	seen := map[string]bool{}
	var keys []string
	for _, part := range strings.Split(raw, "&") {
		k, _, _ := strings.Cut(part, "=")
		if k != "" && k != "token" && !seen[k] {
			seen[k] = true
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}
