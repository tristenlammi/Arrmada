package notify

import (
	"net/url"
	"strings"
)

// Apprise URLs carry their credentials inline: a Discord webhook token, an SMTP
// password, an ntfy access token. Once saved, a URL never leaves the server again —
// the API shows a hint instead, and errors that might quote it are scrubbed.

const mask = "••••"

// hostShownSchemes are the schemes whose host names a server the owner runs or picked
// (a webhook endpoint, a self-hosted ntfy or Gotify, a mail server), not a secret.
// Everything else (discord, tgram, slack, pover, pbul, …) packs tokens where the host
// would be, so nothing of it is shown.
var hostShownSchemes = map[string]bool{
	"json": true, "jsons": true, "form": true, "forms": true, "xml": true, "xmls": true,
	"webhook": true, "webhooks": true,
	"gotify": true, "gotifys": true,
	"ntfy": true, "ntfys": true,
	"matrix": true, "matrixs": true,
	"mailto": true, "mailtos": true,
}

// URLHint is a recognisable but harmless stand-in for a saved Apprise URL:
// scheme://host/•••• where the host isn't secret, otherwise scheme://•••• plus the last
// four characters of a long enough token. It never includes a username or password,
// a path or a query value.
func URLHint(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	// Split by hand: a Telegram bot token ("123456:ABC…") makes the authority look like
	// host:port, which url.Parse refuses.
	scheme, rest, ok := strings.Cut(raw, "://")
	if !ok || scheme == "" {
		return mask
	}
	scheme = strings.ToLower(scheme)
	if hostShownSchemes[scheme] {
		if u, err := url.Parse(raw); err == nil && u.Hostname() != "" {
			host := u.Hostname()
			if p := u.Port(); p != "" {
				host += ":" + p
			}
			// ntfy://topic (no path) is a topic on ntfy.sh: the "host" is the topic itself,
			// which works like a password, so it's treated as a token below.
			ntfyTopic := (scheme == "ntfy" || scheme == "ntfys") && strings.Trim(u.Path, "/") == ""
			if !ntfyTopic {
				return scheme + "://" + host + "/" + mask
			}
		}
	}
	// The tail of the token, query and fragment dropped, so one long token can be told
	// apart from another without being readable.
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	if i := strings.LastIndex(rest, "@"); i >= 0 { // never userinfo
		rest = rest[i+1:]
	}
	rest = strings.TrimRight(rest, "/")
	if len(rest) >= 12 {
		return scheme + "://" + mask + rest[len(rest)-4:]
	}
	return scheme + "://" + mask
}

// Redact scrubs every given URL — whole, and each credential-looking piece of it — out
// of s. Apprise's own error output can quote the URL it failed on, and that text ends
// up in logs, the delivery log and Test's answer.
func Redact(s string, urls ...string) string {
	for _, raw := range urls {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		s = strings.ReplaceAll(s, raw, mask)
		for _, piece := range secretPieces(raw) {
			s = strings.ReplaceAll(s, piece, mask)
		}
	}
	return s
}

// secretPieces splits a URL into the runs of characters between separators, keeping
// those long enough to be a credential (and not the scheme itself). Short pieces are
// left alone so ordinary words in an error message survive.
func secretPieces(raw string) []string {
	var out []string
	// Userinfo is a credential however short it is.
	if u, err := url.Parse(raw); err == nil && u.User != nil {
		if name := u.User.Username(); len(name) >= 3 {
			out = append(out, name)
		}
		if pw, ok := u.User.Password(); ok && len(pw) >= 2 {
			out = append(out, pw)
		}
	}
	scheme := ""
	if i := strings.Index(raw, "://"); i > 0 {
		scheme, raw = strings.ToLower(raw[:i]), raw[i+3:]
	}
	for _, f := range strings.FieldsFunc(raw, func(r rune) bool { return strings.ContainsRune("/:@?&=#,;+ ", r) }) {
		if len(f) >= 5 && strings.ToLower(f) != scheme {
			out = append(out, f)
			if dec, err := url.PathUnescape(f); err == nil && dec != f {
				out = append(out, dec)
			}
		}
	}
	return out
}
