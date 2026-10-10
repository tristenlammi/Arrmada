package main

import (
	"regexp"
	"strings"
)

// The scrubber keeps a capture's shape and drops anything that identifies the server or
// the person: token values, JWTs, the username and email, hostnames, IP addresses and
// the folders the books sit in. Kinds never change (a string stays a string), because the
// shape is the whole point of the capture.

const redacted = "<redacted>"

var (
	jwtRe  = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]*`)
	ipv4Re = regexp.MustCompile(`\b(?:\d{1,3}\.){3}\d{1,3}\b`)
	ipv6Re = regexp.MustCompile(`(?i)[0-9a-f]{0,4}(?::[0-9a-f]{0,4}){2,7}`)
	urlRe  = regexp.MustCompile(`(?i)\b(https?|wss?)://[^/\s"?#]+`)
	winRe  = regexp.MustCompile(`^[A-Za-z]:[\\/]`)
)

// apiPrefixes are absolute paths that are routes, not folders on the server's disk.
var apiPrefixes = []string{"/api/", "/auth/", "/login", "/logout", "/status", "/ping", "/socket.io", "/feed/", "/public/", "/s/", "/hls/"}

type scrubber struct {
	host string
	user string
}

func newScrubber(host, user string) *scrubber {
	return &scrubber{host: strings.ToLower(host), user: user}
}

// value scrubs v, which sits under key in its parent object.
func (s *scrubber) value(key string, v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(t))
		for k, e := range t {
			out[k] = s.value(k, e)
		}
		return out
	case []any:
		out := make([]any, len(t))
		for i, e := range t {
			out[i] = s.value(key, e)
		}
		return out
	case string:
		return s.str(key, t)
	}
	return v
}

func (s *scrubber) str(key, v string) string {
	if v == "" {
		return v
	}
	switch strings.ToLower(key) {
	case "token", "accesstoken", "refreshtoken", "apikey":
		return redacted
	case "username":
		return "listener"
	case "email":
		return "listener@example.invalid"
	case "ipaddress":
		return "192.0.2.1"
	}
	v = jwtRe.ReplaceAllString(v, redacted)
	v = urlRe.ReplaceAllString(v, "${1}://abs.example")
	if s.host != "" {
		v = replaceFold(v, s.host, "abs.example")
	}
	if s.user != "" && len(s.user) >= 3 {
		v = replaceFold(v, s.user, "listener")
	}
	v = ipv4Re.ReplaceAllString(v, "192.0.2.1")
	// An IPv6 address, told apart from a clock time ("12:34:56") by its "::" or its
	// many groups.
	v = ipv6Re.ReplaceAllStringFunc(v, func(m string) string {
		if strings.Contains(m, "::") || strings.Count(m, ":") >= 5 {
			return "2001:db8::1"
		}
		return m
	})
	return scrubPath(v)
}

// scrubPath rewrites a folder on the server's disk to /audiobooks/Book/<file name>, so
// nothing of the server's layout is recorded but a path still reads as one.
func scrubPath(v string) string {
	isWin := winRe.MatchString(v)
	if !isWin && !strings.HasPrefix(v, "/") {
		return v
	}
	if !isWin {
		for _, p := range apiPrefixes {
			if strings.HasPrefix(v, p) {
				return v
			}
		}
		if strings.Count(v, "/") < 2 {
			return v
		}
	}
	norm := strings.ReplaceAll(v, `\`, "/")
	base := norm[strings.LastIndex(norm, "/")+1:]
	if base == "" {
		return "/audiobooks/Book"
	}
	return "/audiobooks/Book/" + base
}

// replaceFold replaces every case-insensitive occurrence of old in s.
func replaceFold(s, old, repl string) string {
	if old == "" {
		return s
	}
	lower, lo := strings.ToLower(s), strings.ToLower(old)
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(lower[i:], lo)
		if j < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		b.WriteString(s[i : i+j])
		b.WriteString(repl)
		i += j + len(old)
	}
}
