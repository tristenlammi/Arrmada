package connstatus

import (
	"regexp"
	"strings"
)

// maxErrorRunes caps a stored error: enough for "login failed — check username/password"
// or a short HTTP error, not enough for a whole HTML error page.
const maxErrorRunes = 300

var (
	// A URL's query string is where Torznab and most trackers carry the API key, so the
	// whole query goes, not just the parameters we know the names of.
	urlQueryRe = regexp.MustCompile(`(?i)(\b[a-z][a-z0-9+.\-]*://[^\s"'<>?#]*)\?[^\s"'<>]*`)
	// Secret-looking key=value pairs outside a URL (a form body echoed in an error, say).
	secretParamRe = regexp.MustCompile(`(?i)\b(apikey|api_key|api-key|passkey|token|access_token|mam_id|password)=[^&\s"'<>;,]*`)
)

// Redact makes an error safe to store and show to staff: URL query strings and secret
// parameters (apikey=, api_key=, passkey=, token=, mam_id=, password=) are removed, and
// the text is capped at 300 characters. Every error the tracker keeps goes through it.
func Redact(msg string) string {
	msg = urlQueryRe.ReplaceAllString(msg, "$1?…")
	msg = secretParamRe.ReplaceAllString(msg, "$1=…")
	msg = strings.TrimSpace(msg)
	if r := []rune(msg); len(r) > maxErrorRunes {
		msg = string(r[:maxErrorRunes-1]) + "…"
	}
	return msg
}
