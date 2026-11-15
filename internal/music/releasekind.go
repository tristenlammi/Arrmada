package music

import (
	"regexp"
	"strings"
)

// A studio album's search finds releases of other albums that share its title: "Artist -
// Album (Live at Wembley)", "Album Demos", "Greatest Hits incl. Album". They pass the title
// check (the album's words are all there), grab, and then can't be imported (#115). A
// release whose name says it's another kind of record — live, demos, a best-of, remixes,
// karaoke or instrumentals — is turned down unless the album is that kind itself (its
// MusicBrainz secondary types) or its own title or artist name says the word.

type releaseKind struct {
	re        *regexp.Regexp
	secondary string // the MusicBrainz secondary type that makes it this album's own kind
	words     string // how the reason names it
}

var releaseKinds = []releaseKind{
	{regexp.MustCompile(`(?i)\b(live|unplugged|in concert)\b`), "Live", "a live recording"},
	{regexp.MustCompile(`(?i)\bdemos?\b`), "Demo", "demos"},
	{regexp.MustCompile(`(?i)\b(greatest hits|best of|the very best|anthology)\b`), "Compilation", "a compilation"},
	{regexp.MustCompile(`(?i)\bremix(es|ed)?\b`), "Remix", "remixes"},
	{regexp.MustCompile(`(?i)\bkaraoke\b`), "", "karaoke"},
	{regexp.MustCompile(`(?i)\binstrumentals?\b`), "", "instrumentals"},
}

// OtherReleaseKind says why a release name is another kind of record than the album, or ""
// when it isn't (or might not be).
func OtherReleaseKind(releaseName, artist string, al Album) string {
	_, secondary := albumTypes(al)
	own := map[string]bool{}
	for _, s := range secondary {
		own[strings.ToLower(s)] = true
	}
	for _, k := range releaseKinds {
		if !k.re.MatchString(releaseName) || k.re.MatchString(al.Title) || k.re.MatchString(artist) {
			continue
		}
		if k.secondary != "" && own[strings.ToLower(k.secondary)] {
			continue
		}
		return "says it's " + k.words + ", and this album isn't"
	}
	return ""
}
