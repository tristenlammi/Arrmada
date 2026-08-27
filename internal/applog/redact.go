package applog

import "strings"

// The Logs page is visible to any manager, exportable, and mirrored to a JSONL file
// that ends up attached to bug reports. An indexer API key or a Plex token that
// reaches a log attribute therefore leaks well beyond the machine — so values whose
// key names them as a secret never make it into the ring at all.
//
// This is a backstop, not a licence: don't log secrets. It catches the accidental
// case (a whole config struct passed as one attr, an upstream URL echoed back in an
// error) where nobody meant to.
var secretKeys = []string{
	"apikey", "api_key", "token", "password", "passwd", "secret",
	"authorization", "cookie", "session_id", "sessionid", "private",
}

// redactKey reports whether an attribute key names a secret.
func redactKey(key string) bool {
	k := strings.ToLower(key)
	// Trim a group prefix ("indexer.api_key") so grouped attrs are caught too.
	if i := strings.LastIndexByte(k, '.'); i >= 0 {
		k = k[i+1:]
	}
	for _, s := range secretKeys {
		if strings.Contains(k, s) {
			return true
		}
	}
	return false
}

// redacted is what replaces a secret's value. Kept distinctive so it's obvious in the
// log that something was withheld rather than empty.
const redacted = "«redacted»"

// scrubQuery blanks the value of a secret-looking query parameter inside a URL that
// appears in a log message. Torznab and MAM both carry the key in the query string,
// so a traced request URL would otherwise print it in full.
func scrubQuery(s string) string {
	if !strings.ContainsRune(s, '?') || !strings.ContainsRune(s, '=') {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	rest := s
	for {
		i := strings.IndexAny(rest, "?&")
		if i < 0 {
			b.WriteString(rest)
			return b.String()
		}
		b.WriteString(rest[:i+1])
		rest = rest[i+1:]
		eq := strings.IndexByte(rest, '=')
		if eq < 0 {
			b.WriteString(rest)
			return b.String()
		}
		key := rest[:eq]
		// The value runs to the next separator, or to whitespace when the URL is
		// embedded in a wider message.
		end := strings.IndexAny(rest[eq+1:], "&\" ")
		if end < 0 {
			end = len(rest) - eq - 1
		}
		b.WriteString(key)
		b.WriteByte('=')
		if redactKey(key) {
			b.WriteString(redacted)
		} else {
			b.WriteString(rest[eq+1 : eq+1+end])
		}
		rest = rest[eq+1+end:]
	}
}
