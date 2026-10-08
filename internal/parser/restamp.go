package parser

import (
	"path/filepath"
	"strings"
)

// Restamping the codec of a converted file.
//
// After Convert re-encodes a file, the recorded source release has to say the new codec,
// or upgrade scoring costs the shrunken file at the old codec's efficiency and the sweep
// re-downloads the very release it came from. Appending " AV1" to the name did not work:
// the old token still read first ("...H.264-GRP AV1" parsed as x264) and the trailing word
// hid the "-GROUP" suffix from reGroup. So the codec token is swapped in place instead.

// codecSpellings are the ways a release name writes a video codec, longest first so
// "h.264" is taken whole rather than stopping at a shorter spelling. Each only counts as a
// whole separator-bounded token: "AVC" inside "XAVC" or "AVCHD" is left alone.
var codecSpellings = []string{
	"h.264", "h 264", "h.265", "h 265",
	"x264", "x265", "h264", "h265", "hevc", "xvid", "divx", "vc-1",
	"avc", "av1", "vc1",
}

// isNameSep reports whether c separates tokens in a release name.
func isNameSep(c byte) bool {
	switch c {
	case '.', ' ', '-', '_', '[', ']', '(', ')', '{', '}', ',', '+':
		return true
	}
	return false
}

type nameSpan struct{ start, end int }

// codecSpans finds every codec token in name, in order. RE2 has no lookaround, so the
// token boundaries are checked by hand.
func codecSpans(name string) []nameSpan {
	lc := strings.ToLower(name) // ASCII lowering keeps byte offsets for these tokens
	var out []nameSpan
	for i := 0; i < len(lc); i++ {
		if i > 0 && !isNameSep(lc[i-1]) {
			continue
		}
		for _, tok := range codecSpellings {
			end := i + len(tok)
			if strings.HasPrefix(lc[i:], tok) && (end == len(lc) || isNameSep(lc[end])) {
				out = append(out, nameSpan{i, end})
				i = end - 1
				break
			}
		}
	}
	// A group that happens to be a codec spelling ("...HDTV.x264-AVC") is the group, not
	// a second codec: restamping must keep it, or the release loses its group.
	if n := len(out); n > 1 {
		last := out[n-1]
		if m := reGroup.FindStringSubmatchIndex(name); m != nil && m[2] == last.start && last.end == len(name) {
			out = out[:n-1]
		}
	}
	return out
}

// codecStamp is the canonical release-name token for a codec.
func codecStamp(c Codec) string {
	switch c {
	case CodecAV1:
		return "AV1"
	case CodecX265:
		return "x265"
	case CodecX264:
		return "x264"
	case CodecXvid:
		return "XviD"
	case CodecVC1:
		return "VC-1"
	}
	return ""
}

// splitVideoExt takes a trailing video container extension off a name, so the codec
// token is placed before ".mkv" rather than after it.
func splitVideoExt(name string) (string, string) {
	ext := filepath.Ext(name)
	for _, v := range videoExts {
		if strings.EqualFold(ext, v) {
			return name[:len(name)-len(ext)], ext
		}
	}
	return name, ""
}

// RestampCodec rewrites a release name to say codec c: the first codec token becomes the
// canonical one ("AV1" or "x265") and any others are dropped, so
// "Movie.2020.1080p.WEB.H.264-GRP" becomes "Movie.2020.1080p.WEB.AV1-GRP" and still parses
// with its group. With no codec token the canonical one goes in just before a trailing
// "-GROUP", or at the end when there is no group. A name that already reads as c is
// returned unchanged.
func RestampCodec(release string, c Codec) string {
	stamp := codecStamp(c)
	if stamp == "" || strings.TrimSpace(release) == "" {
		return release
	}
	name, ext := splitVideoExt(release)
	spans := codecSpans(name)
	if len(spans) == 0 {
		return insertCodec(name, stamp) + ext
	}
	if len(spans) == 1 && Parse(name).Codec == c {
		return release
	}
	var b strings.Builder
	prev := 0
	for i, sp := range spans {
		if i == 0 {
			b.WriteString(name[prev:sp.start])
			b.WriteString(stamp)
			prev = sp.end
			continue
		}
		// Drop a later token with the separator in front of it, so no doubled dots or a
		// dangling space are left behind.
		start := sp.start
		if start > prev && isNameSep(name[start-1]) {
			start--
		}
		b.WriteString(name[prev:start])
		prev = sp.end
	}
	b.WriteString(name[prev:])
	return b.String() + ext
}

// insertCodec adds a codec token to a name that has none: before a trailing "-GROUP" so
// the group still parses, else at the end. A "group" that is really part of the source
// ("WEB-DL") would be split by the insert, so when the source or group would read
// differently afterwards the token is appended instead.
func insertCodec(name, stamp string) string {
	sep := "."
	if strings.Contains(name, " ") && !strings.Contains(name, ".") {
		sep = " "
	}
	if m := reGroup.FindStringSubmatchIndex(name); m != nil {
		out := name[:m[0]] + sep + stamp + name[m[0]:]
		before, after := Parse(name), Parse(out)
		if after.Group == before.Group && after.Source == before.Source && after.Resolution == before.Resolution {
			return out
		}
	}
	return name + sep + stamp
}

// WithoutCodec is a release name with its codec tokens removed, lowercased and with
// separators normalised to single spaces — the identity of a release apart from its
// codec. A converted file's recorded release and the release it was converted from
// compare equal under it.
func WithoutCodec(release string) string {
	name, _ := splitVideoExt(strings.TrimSpace(release))
	var b strings.Builder
	prev := 0
	for _, sp := range codecSpans(name) {
		b.WriteString(name[prev:sp.start])
		b.WriteByte(' ')
		prev = sp.end
	}
	b.WriteString(name[prev:])
	s := strings.ToLower(b.String())
	s = strings.Map(func(r rune) rune {
		if r < 128 && isNameSep(byte(r)) {
			return ' '
		}
		return r
	}, s)
	return strings.Join(strings.Fields(s), " ")
}
